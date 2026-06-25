// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent/lib"
	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
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
}

type fileEntry struct {
	hash           [32]byte
	taskIdentifier lib.TaskIdentifier
	assignee       lib.TaskAssignee
}

// fileOps holds pluggable file I/O functions so the scanner can operate over
// either a local filesystem or the git-rest HTTP API.
type fileOps struct {
	listFiles func(ctx context.Context, glob string) ([]string, error)
	readFile  func(ctx context.Context, relPath string) ([]byte, error)
	writeFile func(ctx context.Context, relPath string, content []byte) error
}

type vaultScanner struct {
	gitClient    gitclient.GitClient
	taskDir      string
	pollInterval time.Duration
	hashes       map[string]fileEntry
	trigger      <-chan struct{}
	metrics      metrics.Metrics
	ops          fileOps
}

// newLocalFileOps creates fileOps backed by the local filesystem rooted at basePath.
func newLocalFileOps(basePath string) fileOps {
	return fileOps{
		listFiles: func(_ context.Context, glob string) ([]string, error) {
			matches, err := filepath.Glob(filepath.Join(basePath, glob))
			if err != nil {
				return nil, err
			}
			rel := make([]string, 0, len(matches))
			for _, m := range matches {
				r, relErr := filepath.Rel(basePath, m)
				if relErr != nil {
					continue
				}
				rel = append(rel, r)
			}
			return rel, nil
		},
		readFile: func(_ context.Context, relPath string) ([]byte, error) {
			return os.ReadFile(
				filepath.Join(basePath, relPath),
			) // #nosec G304 -- basePath is a trusted vault path
		},
		writeFile: func(_ context.Context, relPath string, content []byte) error {
			return os.WriteFile(
				filepath.Join(basePath, relPath),
				content,
				0600,
			) // #nosec G306 -- controlled task file
		},
	}
}

// NewVaultScanner creates a VaultScanner that polls git and scans the task directory.
func NewVaultScanner(
	gitClient gitclient.GitClient,
	taskDir string,
	pollInterval time.Duration,
	trigger <-chan struct{},
	m metrics.Metrics,
) VaultScanner {
	return &vaultScanner{
		gitClient:    gitClient,
		taskDir:      taskDir,
		pollInterval: pollInterval,
		hashes:       make(map[string]fileEntry),
		trigger:      trigger,
		metrics:      m,
		ops:          newLocalFileOps(gitClient.Path()),
	}
}

// NewGitRestVaultScanner creates a VaultScanner that reads and writes vault files
// via the gitclient.GitClient interface methods (ListFiles, ReadFile, WriteFile).
// Use this constructor when git-rest HTTP mode is enabled.
func NewGitRestVaultScanner(
	gitClient gitclient.GitClient,
	taskDir string,
	pollInterval time.Duration,
	trigger <-chan struct{},
	m metrics.Metrics,
) VaultScanner {
	return &vaultScanner{
		gitClient:    gitClient,
		taskDir:      taskDir,
		pollInterval: pollInterval,
		hashes:       make(map[string]fileEntry),
		trigger:      trigger,
		metrics:      m,
		ops: fileOps{
			listFiles: gitClient.ListFiles,
			readFile:  gitClient.ReadFile,
			writeFile: gitClient.WriteFile,
		},
	}
}

func (v *vaultScanner) Run(ctx context.Context, results chan<- ScanResult) error {
	ticker := time.NewTicker(v.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			v.RunCycle(ctx, results)
		case <-v.trigger:
			v.RunCycle(ctx, results)
		}
	}
}

// RunCycle executes a single scan cycle (git pull + file scan + optional commit/push).
// Exported for use in scanner_test package; prefer Run() in production.
func (v *vaultScanner) RunCycle(ctx context.Context, results chan<- ScanResult) {
	if err := v.gitClient.Pull(ctx); err != nil {
		glog.Warningf("git pull failed: %v", err)
		return
	}
	glog.V(3).Infof("git pull succeeded, scanning %s", v.taskDir)

	changed, deleted, written, writeError := v.scanFiles(ctx)

	if len(written) > 0 && !writeError {
		if err := v.gitClient.CommitAndPush(ctx, "[agent-task-controller] update task metadata"); err != nil {
			glog.Warningf("git commit+push failed, skipping publish: %v", err)
			return
		}
	}

	result := ScanResult{Changed: changed, Deleted: deleted}
	select {
	case results <- result:
	default:
	}
}

func (v *vaultScanner) scanFiles(
	ctx context.Context,
) ([]lib.Task, []lib.TaskIdentifier, []string, bool) {
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("list %s failed: %v", glob, err)
		return nil, nil, nil, false
	}
	seen := make(map[string]struct{})
	var changed []lib.Task
	var written []string
	writeError := false
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, nil, nil, false
		default:
		}
		seen[relPath] = struct{}{}
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
	deleted, err := v.collectDeleted(ctx, seen)
	if err != nil {
		glog.V(4).Infof("collectDeleted: %v", err)
	}
	return changed, deleted, written, writeError
}

// processFile handles a single .md file during a scan cycle.
// Returns (task, writtenRelPath, writeError).
//
//nolint:funlen // +5 statements from spec-043 counter calls at 5 skip sites; each site needs its own metric.
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
	taskID, _ := fmMap["task_identifier"].(string)
	currentFMAssignee := frontmatter.Assignee()
	if taskID == "" {
		return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
	}
	if !isValidUUID(taskID) {
		glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
		return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
	}
	if !v.isIdentifierUnique(taskID, relPath) {
		glog.Warningf("replacing duplicate task_identifier %q in %s", taskID, relPath)
		return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
	}
	prevEntry := v.hashes[relPath]

	// Detect empty → named assignee transition (operator re-delegated a parked task).
	if currentFMAssignee != "" && prevEntry.taskIdentifier != "" && prevEntry.assignee == "" {
		wrote, werr := v.writeCounterReset(ctx, relPath, content, fmMap)
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
	}
	if frontmatter.Status() == "" {
		glog.Errorf("skipping %s: invalid frontmatter: status is empty", relPath)
		v.metrics.SkippedFilesTotal(metrics.ReasonEmptyStatus).Inc()
		return nil, "", false
	}
	if currentFMAssignee == "" {
		return nil, "", false
	}
	body := extractBody(content)
	return &lib.Task{
		TaskIdentifier: lib.TaskIdentifier(taskID),
		Frontmatter:    frontmatter,
		Content:        lib.TaskContent(body),
	}, "", false
}

// injectAndStore generates a UUID, writes it into the file via ops.writeFile,
// and records a sentinel hash entry with the file's current assignee.
// Returns (nil task, writtenRelPath, writeError).
func (v *vaultScanner) injectAndStore(
	ctx context.Context,
	content []byte,
	relPath string,
	currentAssignee lib.TaskAssignee,
) (*lib.Task, string, bool) {
	id := uuid.New().String()
	newContent, injectErr := InjectTaskIdentifier(ctx, content, id)
	if injectErr != nil {
		glog.Warningf("skipping %s: failed to inject task_identifier: %v", relPath, injectErr)
		v.metrics.SkippedFilesTotal(metrics.ReasonInjectTaskIdentifierFailed).Inc()
		return nil, "", false
	}
	if writeErr := v.ops.writeFile(ctx, relPath, newContent); writeErr != nil {
		glog.Warningf("failed to write %s: %v", relPath, writeErr)
		return nil, "", true
	}
	v.hashes[relPath] = fileEntry{
		hash:           [32]byte{},
		taskIdentifier: lib.TaskIdentifier(id),
		assignee:       currentAssignee,
	}
	return nil, relPath, false
}

// writeCounterReset rewrites the task file with trigger_count: 0 and retry_count: 0.
// fmMap is the already-parsed frontmatter map for this file.
// Returns (relPath, false) on success, ("", true) on write error.
func (v *vaultScanner) writeCounterReset(
	ctx context.Context,
	relPath string,
	content []byte,
	fmMap map[string]interface{},
) (string, bool) {
	resetFm := make(map[string]interface{}, len(fmMap))
	for k, val := range fmMap {
		resetFm[k] = val
	}
	resetFm["trigger_count"] = 0
	resetFm["retry_count"] = 0

	newFmYAML, err := yaml.Marshal(resetFm)
	if err != nil {
		glog.Warningf("writeCounterReset: marshal failed for %s: %v", relPath, err)
		return "", false
	}

	body := extractBody(content)
	newContent := []byte("---\n" + string(newFmYAML) + "---\n" + body)

	if writeErr := v.ops.writeFile(ctx, relPath, newContent); writeErr != nil {
		glog.Warningf("writeCounterReset: write failed for %s: %v", relPath, writeErr)
		return "", true
	}
	glog.V(2).Infof("writeCounterReset: reset trigger_count/retry_count for %s", relPath)
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
			deleted = append(deleted, entry.taskIdentifier)
			delete(v.hashes, relPath)
		}
	}
	return deleted, nil
}
