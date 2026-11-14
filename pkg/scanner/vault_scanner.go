// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	"github.com/golang/glog"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// ScanResult holds the outcome of a single vault scan cycle.
type ScanResult struct {
	Changed []lib.Task           // tasks whose content changed (new or modified)
	Deleted []lib.TaskIdentifier // task identifiers that were previously known but are now gone
}

//counterfeiter:generate -o ../../mocks/vault_scanner.go --fake-name VaultScanner . VaultScanner
type VaultScanner interface {
	// Run starts the polling loop. Blocks until ctx is cancelled.
	// Results are sent to the provided channel; the caller owns the channel.
	Run(ctx context.Context, results chan<- ScanResult) error
	// RunCycle executes a single scan cycle (git pull + file scan + optional commit/push).
	// Exported for use in scanner_test package; prefer Run() in production.
	RunCycle(ctx context.Context, results chan<- ScanResult)
	// Resolve returns the vault-relative path of the task file carrying id, from the
	// identifier→path index the last completed scan cycle — or, in trigger-only mode,
	// the boot build — published, including entries recorded since that rebuild.
	// It returns ("", false, nil) when the index does not hold id — an unknown
	// identifier is not an error — and ("", false, err) naming both paths when two
	// files carry id. The index is empty until the first cycle completes.
	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
	// Record tells the resolver that relPath is the vault-relative path of the task
	// file carrying id, so a later Resolve(id) hits instead of missing. The caller
	// must have just established that fact — by writing that file or by a walk that
	// found exactly one file carrying id. It returns nothing: recording is a cache
	// fill, and a failure to record only costs a later walk. An empty id or empty
	// relPath is ignored. A resolver that already knows of two or more paths for id
	// keeps them, so a known duplicate is never hidden.
	Record(ctx context.Context, id lib.TaskIdentifier, relPath string)
	// RecordDuplicate adds relPath alongside the path(s) already known for id, where
	// Record would replace them, so a second file carrying id makes Resolve report
	// the duplicate as an error. See result.TaskPathResolver.RecordDuplicate for the
	// contract.
	RecordDuplicate(ctx context.Context, id lib.TaskIdentifier, relPath string)
}

type fileEntry struct {
	hash           [32]byte
	taskIdentifier lib.TaskIdentifier
	assignee       lib.TaskAssignee
	// capped records that the file's frontmatter is at or past its trigger cap
	// (see isCappedIdle). It is populated whenever the file is parsed, which for a
	// stable capped record is the last time it changed — exactly what the sweep
	// needs, because a capped record stops changing precisely when the executor
	// stops writing to it.
	capped bool
	// phase is the lifecycle phase last published for this file. It exists so a
	// phase transition is still published after the park path clears the assignee:
	// without it processFile stored the new hash and then returned early on an
	// empty assignee, so the view never learned the phase moved and
	// view_divergence reported the task forever (2026-11-03).
	phase string
}

// fileOps is the scanner's read seam. It is file-shaped on purpose: every helper
// in this package (task age, open-task census, capped-idle sweep, stuck-planning
// gauge) asks for a vault-relative path and gets that file's bytes, and the
// typed client is adapted to that shape in one place rather than in seven.
//
// There is no writeFile: a write is now a typed mutation (PatchTask, or
// ReaddressTask for the identifier repair), and a path-addressed write is exactly
// what this migration removes.
type fileOps struct {
	listFiles func(ctx context.Context, glob string) ([]string, error)
	readFile  func(ctx context.Context, relPath string) ([]byte, error)
}

type vaultScanner struct {
	client          vaultapi.Client
	taskDir         string
	pollInterval    time.Duration
	hashes          map[string]fileEntry
	trigger         <-chan struct{}
	metrics         metrics.Metrics
	ops             fileOps
	autoInject      bool
	vaultName       string
	metricsInterval time.Duration

	// openTaskPairs is the set of (phase, task_type) pairs the previous census pass
	// published, so the next pass can delete the ones that vanished. It is a nil map
	// — a valid empty set — until the first successful pass, which is why neither
	// constructor initialises it. Single-goroutine: the census runs inline in Run's
	// goroutine and never overlaps itself.
	openTaskPairs map[openTaskPair]struct{}

	// indexMutex guards index. publishIndex builds the whole map and swaps it under
	// the write lock; Resolve reads the published snapshot under the read lock. A
	// reader therefore observes either the previous cycle's map or the new one,
	// never a partially built map and never one being mutated. The scanner's own
	// bookkeeping (hashes) stays single-goroutine and unguarded — only this
	// published snapshot is shared. In-process only, never serialized.
	indexMutex sync.RWMutex
	// index maps a task identifier to every vault-relative path carrying it. The
	// slice is a multimap entry on purpose: collapsing a duplicate to one path is
	// the 2026-08-31 incident (a result landing on the wrong task file). Rebuilt
	// wholesale once per completed scan cycle (or once by buildIndex in trigger-only
	// mode); between rebuilds a single entry is replaced by Record, or extended by
	// RecordDuplicate when a create proves a second file carries the identifier —
	// always with a new slice, never mutated in place, because Resolve uses the slice it read after
	// releasing the read lock. A Record that lands just before a publishIndex or
	// buildIndex swap is lost with the old map, which costs at most one later walk.
	index map[lib.TaskIdentifier][]string
}

// typedFileOps adapts the typed vault API to the file-shaped seam above.
//
// ⚠️ **The path is derived, not reported.** The typed client returns tasks, not
// files, and there is no path on a task; the vault-relative path is composed from
// the task's title exactly as `pkg/command/planning_retry.go:taskRelPath` already
// does. It is a KEY for the scanner's bookkeeping and a human-readable label — it
// is never handed to a write, because every write is addressed by identifier. A
// file whose name differs from its title therefore gets an approximate path here,
// which is harmless precisely because nothing writes to it.
//
// The last listing is kept so readFile can answer a path it was just given
// without a second round trip, and so the bytes it returns are a faithful
// rendering of the task the API described rather than a re-read that may have
// moved underneath the cycle.
type typedFileOps struct {
	client  vaultapi.Client
	taskDir string

	mutex  sync.RWMutex
	byPath map[string]vaultapi.Task
}

func newTypedFileOps(client vaultapi.Client, taskDir string) fileOps {
	typed := &typedFileOps{
		client:  client,
		taskDir: taskDir,
		byPath:  make(map[string]vaultapi.Task),
	}
	return fileOps{listFiles: typed.list, readFile: typed.read}
}

// taskRelPath composes the vault-relative path a task's title implies. An empty
// title has no path, and the caller skips it: a task the API could not name is
// not one the scanner can key.
func taskRelPath(taskDir string, task vaultapi.Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		return ""
	}
	return filepath.Join(taskDir, title+".md")
}

func (t *typedFileOps) list(_ context.Context, _ string) ([]string, error) {
	tasks, err := t.client.ListTasks(context.Background(), vaultapi.Filter{})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(tasks))
	byPath := make(map[string]vaultapi.Task, len(tasks))
	for _, task := range tasks {
		relPath := taskRelPath(t.taskDir, task)
		if relPath == "" {
			continue
		}
		paths = append(paths, relPath)
		byPath[relPath] = task
	}
	t.mutex.Lock()
	t.byPath = byPath
	t.mutex.Unlock()
	return paths, nil
}

// read renders the task last listed at relPath as the markdown this package
// parses. Rendering rather than re-fetching keeps the cycle internally
// consistent: every helper in one pass sees the same snapshot.
func (t *typedFileOps) read(_ context.Context, relPath string) ([]byte, error) {
	t.mutex.RLock()
	task, ok := t.byPath[relPath]
	t.mutex.RUnlock()
	if !ok {
		// The sweep reads a path the last listing may not have covered — a record
		// created after that listing, or one read by a pass that never listed. The
		// path carries the task's title, which is an address the API resolves, so a
		// miss falls back to one typed read rather than failing the escalation.
		found, readErr := t.client.ReadTask(context.Background(), taskAddressFromPath(relPath))
		if readErr != nil {
			return nil, errors.Wrapf(
				context.Background(), readErr, "no task listed or readable at %s", relPath,
			)
		}
		task = *found
	}
	return renderTask(context.Background(), task)
}

// renderTask writes a task back as the frontmatter block plus body this package
// already knows how to parse.
func renderTask(ctx context.Context, task vaultapi.Task) ([]byte, error) {
	frontmatter := task.Frontmatter
	if frontmatter == nil {
		frontmatter = map[string]any{}
	}
	rendered, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal frontmatter for %s", task.ID)
	}
	return []byte("---\n" + string(rendered) + "---\n" + task.Body), nil
}

// NewVaultScanner creates a VaultScanner that reads the vault through the typed
// API. There is one constructor, not two: the local-file-ops variant existed only
// to serve a clone this controller no longer holds, and a second constructor would
// be a dead path the grep criterion cannot see.
func NewVaultScanner(
	client vaultapi.Client,
	taskDir string,
	pollInterval time.Duration,
	trigger <-chan struct{},
	m metrics.Metrics,
	autoInject bool,
	vaultName string,
	metricsInterval time.Duration,
) VaultScanner {
	return &vaultScanner{
		client:          client,
		taskDir:         taskDir,
		pollInterval:    pollInterval,
		hashes:          make(map[string]fileEntry),
		trigger:         trigger,
		metrics:         m,
		ops:             newTypedFileOps(client, taskDir),
		autoInject:      autoInject,
		vaultName:       vaultName,
		metricsInterval: metricsInterval,
		index:           make(map[lib.TaskIdentifier][]string),
	}
}

func (v *vaultScanner) Run(ctx context.Context, results chan<- ScanResult) error {
	// A non-positive poll interval disables the periodic scan: the vault is then
	// scanned only when something sends on trigger. time.NewTicker panics on a
	// non-positive duration, so no ticker is constructed at all and the nil
	// channel blocks forever — which is exactly "never ticks".
	var tick <-chan time.Time
	if v.pollInterval > 0 {
		ticker := time.NewTicker(v.pollInterval)
		defer ticker.Stop()
		tick = ticker.C
	} else {
		// Trigger-only mode has no cycle to publish the identifier→path index, and
		// nothing in that deployment fires the trigger: the index then stays empty
		// for the pod's whole life, every lookup misses, and each command pays the
		// full directory walk. Build it once here so a lookup for a file that
		// already existed is one read; a cycle, when one does run, still
		// republishes it from its own bookkeeping.
		v.buildIndex(ctx)
	}
	// A non-positive metrics interval constructs neither the ticker nor the start-up
	// recompute, mirroring the poll-interval guard above: time.NewTicker panics on a
	// non-positive duration. Only a caller can produce the value; no configuration
	// exposes it.
	var metricsTick <-chan time.Time
	if v.metricsInterval > 0 {
		// One recompute before the loop, so a fresh pod publishes both the age and
		// the open-task census series immediately instead of reporting "no data"
		// for the first interval. One pass publishes both: they are still separate
		// instruments with separate definitions and separate skip rules, but they
		// read the same file facts, so a second listing and a second read of every
		// task file bought nothing and cost a full extra pass over the vault.
		//
		// The same pass drives the capped-idle sweep, which is what makes the sweep
		// recur on a trigger-only controller: see sweepCappedIdle.
		v.sweepCappedIdle(ctx)
		metricsTicker := time.NewTicker(v.metricsInterval)
		defer metricsTicker.Stop()
		metricsTick = metricsTicker.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
			v.RunCycle(ctx, results)
		case <-v.trigger:
			v.RunCycle(ctx, results)
		case <-metricsTick:
			v.sweepCappedIdle(ctx)
		}
	}
}

// RunCycle executes a single scan cycle (list + file scan).
// Exported for use in scanner_test package; prefer Run() in production.
//
// There is no pull and no commit: vault-api owns the working copy and commits
// every write server-side, so the cycle is one listing plus the repairs it
// decides to make. A repair that fails no longer withholds the publish — the
// failure is already visible in the metrics and the log, and the records the
// cycle did read are still worth publishing.
func (v *vaultScanner) RunCycle(ctx context.Context, results chan<- ScanResult) {
	glog.V(3).Infof("scanning %s", v.taskDir)

	changed, deleted, repaired, repairError, listed := v.scanFiles(ctx)
	if !listed {
		// A cycle that could not list the vault publishes nothing: reporting an
		// empty result would read downstream as "every task disappeared".
		return
	}

	if len(repaired) > 0 && repairError {
		glog.Warningf("scan cycle repaired %d task(s) with at least one failure", len(repaired))
	}

	result := ScanResult{Changed: changed, Deleted: deleted}
	select {
	case results <- result:
	default:
	}
}

func (v *vaultScanner) scanFiles(
	ctx context.Context,
) ([]lib.Task, []lib.TaskIdentifier, []string, bool, bool) {
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("list %s failed: %v", glob, err)
		return nil, nil, nil, false, false
	}
	// Build the cycle's path set and prune vanished paths BEFORE processing any
	// file. isIdentifierUnique answers against v.hashes, so a rename's old path
	// must be gone from the bookkeeping before the new path is checked; otherwise
	// the renamed file collides with its own former path and is re-minted.
	seen := make(map[string]struct{}, len(paths))
	for _, relPath := range paths {
		seen[relPath] = struct{}{}
	}
	deleted, err := v.collectDeleted(ctx, seen)
	if err != nil {
		glog.V(4).Infof("collectDeleted: %v", err)
	}
	var changed []lib.Task
	var written []string
	writeError := false
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, nil, nil, false, false
		default:
		}
		task, wrote, werr := v.processFile(ctx, relPath)
		if werr {
			writeError = true
		}
		if wrote != "" {
			written = append(written, wrote)
		}
		if task != nil {
			changed = append(changed, *task)
		}
	}
	// Runs after the per-file loop so the index it walks is this cycle's. Escalating
	// here, rather than inside processFile, is what reaches a record the executor has
	// stopped touching: processFile returns early on an unchanged hash, so a stable
	// capped record would otherwise never be looked at again.
	escalated, escalateError := v.escalateCappedIdleTasks(ctx)
	if escalateError {
		writeError = true
	}
	if escalated > 0 {
		glog.Infof("escalateCappedIdle: escalated %d capped task(s)", escalated)
	}
	v.metrics.UpdateGoStuckPlanningTasks().
		Set(float64(v.countStuckPlanningTasks(ctx, paths, time.Now())))
	v.publishIndex()
	return changed, deleted, written, writeError, true
}

// publishIndex rebuilds the identifier→path index from the scanner's bookkeeping and
// publishes it for readers, replacing the previous snapshot wholesale under the write
// lock.
//
// It is called once per completed scan cycle, after the deleted-file collection has
// returned, so a path that cycle deleted is already gone from the bookkeeping and the
// published map never holds a deleted file. The map is built completely before the
// swap, so a reader can never observe a partially built index.
//
// An entry whose identifier is empty is skipped: a halted repair stores an empty
// identifier (injectAndStore), and an empty identifier is not a task — it must never
// be resolvable, exactly as collectDeleted refuses to emit it downstream.
//
// A duplicate identifier keeps BOTH paths in the slice; Resolve reports the ambiguity
// as an error rather than picking one. That branch is defence-in-depth rather than the
// primary guard: isIdentifierUnique keeps hashes duplicate-free by repairing or
// skipping a colliding file before it is stored, so two paths for one identifier are
// unreachable while that invariant holds. The multimap exists so that relaxing the
// invariant fails loudly instead of silently collapsing to one path.
func (v *vaultScanner) publishIndex() {
	index := make(map[lib.TaskIdentifier][]string, len(v.hashes))
	for relPath, entry := range v.hashes {
		if entry.taskIdentifier == "" {
			continue
		}
		index[entry.taskIdentifier] = append(index[entry.taskIdentifier], relPath)
	}
	v.indexMutex.Lock()
	defer v.indexMutex.Unlock()
	v.index = index
}

// buildIndex populates the identifier→path index from one read-only pass over the
// task directory, without running a scan cycle.
//
// A scan cycle is the only other publisher of this index, and a deployment that sets
// a non-positive POLL_INTERVAL never runs one: the vault is then scanned only on a
// trigger, and nothing in that deployment fires the trigger. The index therefore
// stays empty for the pod's whole life, every Resolve misses, and every command pays
// the full directory walk — measured on the prod openclaw controller at 0 hits /
// 606 misses and a median 18 s per lookup over 17 h, which serialized the
// single-threaded command consumer and pushed tail commands past the 60-minute
// expiry. Building it once at start costs that same single pass and turns every
// later lookup for a file that already existed into one read.
//
// Deliberately narrower than scanFiles: it does not pull, inject a missing
// task_identifier, write, commit or publish a changed task, so trigger-only
// deployments keep their "no vault writes from the scanner" property and no
// vault's full task set is republished as changed on every restart. A file without
// a valid identifier is skipped rather than repaired — the index is a lookup cache,
// not a repair path, and the walk stays the recovery. Files created after this pass
// are added by Record — on create, or on their first walk; the bookkeeping (hashes)
// is left untouched so a later cycle's change detection is unchanged.
func (v *vaultScanner) buildIndex(ctx context.Context) {
	// A log-only elapsed measurement, matching the direct time.Now scanFiles already
	// uses for its own timing: no clock value reaches a task, a write or a decision.
	start := time.Now()
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("buildIndex: list %s failed: %v", glob, err)
		return
	}
	index := make(map[lib.TaskIdentifier][]string, len(paths))
	// Records already at their trigger cap with a live assignee, collected during
	// the same parse this pass already does for task_identifier. This is the ONLY
	// place the sweep can see them on a trigger-only controller.
	var cappedAssigned []string
	skipped := 0
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return
		default:
		}
		content, readErr := v.ops.readFile(ctx, relPath)
		if readErr != nil {
			skipped++
			continue
		}
		fmYAML, fmErr := extractFrontmatter(ctx, content)
		if fmErr != nil {
			skipped++
			continue
		}
		var fmMap map[string]interface{}
		if umErr := yaml.Unmarshal([]byte(fmYAML), &fmMap); umErr != nil {
			skipped++
			continue
		}
		taskID, _ := fmMap["task_identifier"].(string)
		if taskID == "" || !isValidUUID(taskID) {
			skipped++
			continue
		}
		id := lib.TaskIdentifier(taskID)
		index[id] = append(index[id], relPath)
		if fm := lib.TaskFrontmatter(fmMap); isCappedIdle(fm) && fm.Assignee() != "" {
			cappedAssigned = append(cappedAssigned, relPath)
		}
	}
	v.indexMutex.Lock()
	v.index = index
	v.indexMutex.Unlock()
	glog.V(2).Infof(
		"buildIndex: %d identifier(s) from %d file(s) in %v (%d skipped)",
		len(index),
		len(paths),
		time.Since(start),
		skipped,
	)
	// A trigger-only controller never scans, so without this the sweep would never
	// run where it is needed most. This pass has already read and parsed every file,
	// so the candidate list costs nothing extra.
	if escalated, werr := v.escalateCappedPaths(ctx, cappedAssigned); escalated > 0 || werr {
		glog.Infof(
			"escalateCappedIdle: escalated %d capped task(s) during index build (%d candidate(s))",
			escalated,
			len(cappedAssigned),
		)
	}
}

// Resolve returns the vault-relative path of the task file carrying id, from the
// index the last completed scan cycle published.
//
// The empty identifier is never indexed, so it is always reported absent.
// Two paths for one identifier is unresolvable — picking either one writes a result
// onto a file that may belong to a different task (2026-08-31) — so it is reported as
// an error naming both paths, never as a miss.
func (v *vaultScanner) Resolve(
	ctx context.Context,
	id lib.TaskIdentifier,
) (string, bool, error) {
	if id == "" {
		return "", false, nil
	}
	v.indexMutex.RLock()
	paths := v.index[id]
	v.indexMutex.RUnlock()
	switch len(paths) {
	case 0:
		return "", false, nil
	case 1:
		return paths[0], true, nil
	default:
		return "", false, errors.Errorf(
			ctx,
			"duplicate task_identifier %s in %s and %s",
			id,
			paths[0],
			paths[1],
		)
	}
}

// Record adds one identifier→path entry to the published index between rebuilds, so
// a task created after the last rebuild (or found by a walk) resolves with one read
// instead of another full-vault walk. See result.TaskPathResolver.Record for the
// contract.
func (v *vaultScanner) Record(ctx context.Context, id lib.TaskIdentifier, relPath string) {
	if id == "" || relPath == "" {
		return
	}
	v.indexMutex.Lock()
	defer v.indexMutex.Unlock()
	if len(v.index[id]) >= 2 {
		// A known duplicate stays reported as one (2026-08-31 incident).
		return
	}
	// Store a NEW slice: Resolve uses the slice it read after releasing the
	// read lock, so an existing slice must never be modified in place.
	v.index[id] = []string{relPath}
	glog.V(2).Infof("index: recorded %s for task %s", relPath, id)
}

// RecordDuplicate adds relPath beside the path(s) the index already holds for id.
// Record replaces a single entry, which is right after a walk proves a file unique
// but hides a second file a create writes for an identifier already indexed
// elsewhere: a trigger-only controller never rebuilds, so later writes for id would
// keep landing on the old file (2026-10-27, identifier 9371a3d2: the spawn write hit
// an aborted file while the live one stayed invisible). See
// result.TaskPathResolver.RecordDuplicate for the contract.
func (v *vaultScanner) RecordDuplicate(
	ctx context.Context,
	id lib.TaskIdentifier,
	relPath string,
) {
	if id == "" || relPath == "" {
		return
	}
	v.indexMutex.Lock()
	defer v.indexMutex.Unlock()
	known := v.index[id]
	if slices.Contains(known, relPath) {
		return
	}
	// Store a NEW slice, as Record does: Resolve uses the slice it read after
	// releasing the read lock, so an existing slice must never be modified in place.
	paths := make([]string, 0, len(known)+1)
	paths = append(paths, known...)
	paths = append(paths, relPath)
	v.index[id] = paths
	glog.V(2).Infof("index: recorded %s beside %v for task %s", relPath, known, id)
}

// processFile handles a single .md file during a scan cycle.
// Returns (task, writtenRelPath, writeError).
//
//nolint:funlen,gocognit // +5 statements from spec-043 counter calls at 5 skip sites; each site needs its own metric.; +21 lines from spec-001 per-site auto-inject gate; inlined per spec-001 prompt 2 to keep the parity-check awk range honest.; +4 lines from spec-008 present-but-invalid task_identifier branch (key-absent and present-invalid auto-inject gates kept separate; parity-check awk count was 9 at spec-008).; spec-009 adds no statements to processFile (the convergence guard lives in injectAndStore), but raises the parity-check awk count from 9 to 10 via the convergence-halt log+counter site.
func (v *vaultScanner) processFile(
	ctx context.Context,
	relPath string,
) (*lib.Task, string, bool) {
	content, readErr := v.ops.readFile(ctx, relPath)
	if readErr != nil {
		glog.Warningf("failed to read %s: %v", relPath, readErr)
		v.metrics.SkippedFilesTotal(metrics.ReasonReadFailed).Inc()
		return nil, "", false
	}
	hash := sha256.Sum256(content)
	if existing, ok := v.hashes[relPath]; ok && existing.hash == hash {
		return nil, "", false
	}
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		glog.Errorf("skipping %s: invalid frontmatter: %v", relPath, err)
		v.metrics.SkippedFilesTotal(metrics.ReasonInvalidFrontmatter).Inc()
		return nil, "", false
	}
	dedupedYAML, hasDuplicates, dedupErr := DeduplicateFrontmatter(ctx, fmYAML)
	if dedupErr != nil {
		glog.Errorf("skipping %s: invalid frontmatter: %v", relPath, dedupErr)
		v.metrics.SkippedFilesTotal(metrics.ReasonDuplicateFrontmatterInvalid).Inc()
		return nil, "", false
	}
	if hasDuplicates {
		glog.Warningf("file %s has duplicate frontmatter keys, deduplicating", relPath)
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(dedupedYAML), &fmMap); err != nil {
		glog.Errorf("skipping %s: invalid frontmatter: %v", relPath, err)
		v.metrics.SkippedFilesTotal(metrics.ReasonInvalidFrontmatter).Inc()
		return nil, "", false
	}
	frontmatter := lib.TaskFrontmatter(fmMap)
	rawTaskID, present := fmMap["task_identifier"]
	taskID, isString := rawTaskID.(string)
	currentFMAssignee := frontmatter.Assignee()
	if !present {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		return v.repairIdentifier(ctx, relPath, currentFMAssignee, hash, "")
	}
	if !isString || !isValidUUID(taskID) {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		if isString {
			// %T would print just "string" and lose the offending value, which
			// is the only thing that tells an operator which file content to
			// look for. Quoted so an empty or whitespace-only value is visible.
			glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
		} else {
			// Non-string values (sequences, mappings, ints) are logged by type
			// only: a large sequence or mapping must not blow up a log line.
			glog.Warningf("replacing invalid task_identifier of type %T in %s", rawTaskID, relPath)
		}
		previous := recordablePrevious(taskID, isString)
		return v.repairIdentifier(ctx, relPath, currentFMAssignee, hash, previous)
	}
	if !v.isIdentifierUnique(taskID, relPath) {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		glog.Warningf("replacing duplicate task_identifier %q in %s", taskID, relPath)
		return v.repairIdentifier(ctx, relPath, currentFMAssignee, hash, "")
	}
	prevEntry := v.hashes[relPath]
	currentPhase := phaseString(frontmatter)

	// Detect empty → named assignee transition (operator re-delegated a parked task).
	if currentFMAssignee != "" && prevEntry.taskIdentifier != "" && prevEntry.assignee == "" {
		wrote, werr := v.writeCounterReset(ctx, relPath, taskID)
		if werr {
			return nil, "", true
		}
		if wrote != "" {
			// Store zero-hash sentinel so next scan re-processes and publishes the task.
			// Store new assignee so the transition is not re-triggered on the next pass.
			v.hashes[relPath] = fileEntry{
				hash:           [32]byte{},
				taskIdentifier: lib.TaskIdentifier(taskID),
				assignee:       currentFMAssignee,
			}
			return nil, wrote, false
		}
	}

	// Normal path: update stored entry with current state.
	v.hashes[relPath] = fileEntry{
		hash:           hash,
		taskIdentifier: lib.TaskIdentifier(taskID),
		assignee:       currentFMAssignee,
		capped:         isCappedIdle(frontmatter),
		phase:          currentPhase,
	}
	if frontmatter.Status() == "" {
		glog.Errorf("skipping %s: invalid frontmatter: status is empty", relPath)
		v.metrics.SkippedFilesTotal(metrics.ReasonEmptyStatus).Inc()
		return nil, "", false
	}
	if currentFMAssignee == "" {
		// The park path clears the assignee in the same write that moves the phase
		// to human_review (result.ClearAssigneeIfHumanReview), so the record
		// carrying the transition is exactly the one this gate used to drop — and
		// the hash was already stored above, so the file was never looked at again.
		// The view could therefore never converge, which is what view_divergence
		// reported.
		//
		// Publish the transition, but only a transition: a record already parked at
		// this phase publishes nothing, so a full-vault scan does not flood the
		// event topic with every parked, escalated and terminal task. Publishing is
		// not spawning — the executor's trigger-phase gate refuses a human_review
		// record before the parked-assignee restore is reached.
		if prevEntry.phase == "" || prevEntry.phase == currentPhase {
			return nil, "", false
		}
	}
	body := extractBody(content)
	return &lib.Task{
		TaskIdentifier: lib.TaskIdentifier(taskID),
		Frontmatter:    frontmatter,
		Content:        lib.TaskContent(body),
	}, "", false
}

// phaseString renders the frontmatter's lifecycle phase as a comparable string,
// or "" when the key is absent. Phase() returns a pointer so an absent key is
// distinguishable from an empty one; both collapse to "" here because the only
// consumer compares one scan's value against the previous scan's.
func phaseString(frontmatter lib.TaskFrontmatter) string {
	if p := frontmatter.Phase(); p != nil {
		return string(*p)
	}
	return ""
}

// repairIdentifier mints a new task_identifier for the file at relPath through
// the typed re-address verb, and records a sentinel hash entry with the file's
// current assignee so the next cycle publishes it.
//
// When previous is non-empty it travels in the SAME request, so the replaced
// address keeps resolving for a result already in flight — the mechanism behind
// agent-task-controller#127. A separate write would leave a window where neither
// address resolves.
//
// The old code re-parsed its own candidate bytes through repairConverges before
// writing, because a write it could not prove had cleared the trigger was worse
// than no write. That guard is gone with the write: the API answers with the task
// it stored, so a success IS the convergence proof, and a refusal is a failure
// this function reports rather than one it must predict.
//
// On failure the on-disk hash is stored anyway, so the repair rides the existing
// content-hash short-circuit and re-arms only when the file's bytes change — the
// same halt semantics the convergence guard provided.
//
// Returns (nil task, writtenRelPath, writeError).
func (v *vaultScanner) repairIdentifier(
	ctx context.Context,
	relPath string,
	currentAssignee lib.TaskAssignee,
	onDiskHash [32]byte,
	previous string,
) (*lib.Task, string, bool) {
	id := uuid.New().String()
	address := taskAddressFromPath(relPath)
	if _, err := v.client.ReaddressTask(ctx, address, id, previous); err != nil {
		glog.Warningf("failed to repair task_identifier for %s: %v", relPath, err)
		v.metrics.SkippedFilesTotal(metrics.ReasonInjectTaskIdentifierFailed).Inc()
		v.hashes[relPath] = fileEntry{
			hash:     onDiskHash,
			assignee: currentAssignee,
		}
		return nil, "", true
	}
	v.hashes[relPath] = fileEntry{
		hash:           [32]byte{},
		taskIdentifier: lib.TaskIdentifier(id),
		assignee:       currentAssignee,
	}
	return nil, relPath, false
}

// splitRendered parses a rendered task back into its frontmatter map and body.
// It is the inverse of renderTask, and the only place a write path needs to see
// a task as fields rather than as bytes.
func splitRendered(ctx context.Context, content []byte) (map[string]any, string, error) {
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "extract frontmatter")
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &fields); err != nil {
		return nil, "", errors.Wrapf(ctx, err, "unmarshal frontmatter")
	}
	return fields, extractBody(content), nil
}

// changedFields returns the keys whose value differs between the two maps, which
// is what a typed PATCH wants: it replaces only the keys it is given, so sending
// the whole frontmatter would rewrite fields this path never touched.
func changedFields(current, next map[string]any) map[string]any {
	fields := make(map[string]any)
	for key, value := range next {
		if existing, ok := current[key]; ok && reflect.DeepEqual(existing, value) {
			continue
		}
		fields[key] = value
	}
	return fields
}

// stringValue reads a frontmatter key that is expected to be a string.
func stringValue(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

// taskAddressFromPath derives the address the API resolves for a vault-relative
// task path: its title, which is the filename without the directory and suffix.
// The API keys identifier, name and slug in one space, so a file carrying an
// invalid identifier still resolves by this address — which is exactly the file
// the repair exists for.
func taskAddressFromPath(relPath string) string {
	return strings.TrimSuffix(filepath.Base(relPath), ".md")
}

// writeCounterReset resets trigger_count to 0 through the typed PATCH, which
// replaces only the keys it names and serialises per task. That is the atomicity
// the 2026-10-19 fix bought: the previous whole-file write from the scan-time
// snapshot raced the executor's trigger_count increment and produced the
// frontmatter conflict git-rest quarantined (commit 950df8df47, trigger_count 3
// vs 0). The read-modify-write loop is gone with the write — PATCH is the atomic
// unit now, so there is no snapshot to race.
// Returns (relPath, false) on success, ("", true) on write error.
func (v *vaultScanner) writeCounterReset(
	ctx context.Context,
	relPath string,
	taskID string,
) (string, bool) {
	if _, err := v.client.PatchTask(ctx, taskID, map[string]any{"trigger_count": 0}); err != nil {
		glog.Warningf("writeCounterReset: patch failed for %s: %v", relPath, err)
		return "", true
	}
	glog.V(2).Infof("writeCounterReset: reset trigger_count for %s", relPath)
	return relPath, false
}

func (v *vaultScanner) collectDeleted(
	ctx context.Context,
	seen map[string]struct{},
) ([]lib.TaskIdentifier, error) {
	var deleted []lib.TaskIdentifier
	for relPath, entry := range v.hashes {
		select {
		case <-ctx.Done():
			return deleted, errors.Wrap(ctx, ctx.Err(), "collectDeleted cancelled")
		default:
		}
		if _, ok := seen[relPath]; !ok {
			delete(v.hashes, relPath)
			// A halted repair has no identifier to record, so its entry carries an
			// empty lib.TaskIdentifier. Dropping the bookkeeping is correct; emitting
			// the empty identifier downstream is not — an empty identifier is not a
			// task, and it must never reach the deleted-tasks stream.
			if entry.taskIdentifier == "" {
				continue
			}
			deleted = append(deleted, entry.taskIdentifier)
		}
	}
	return deleted, nil
}
