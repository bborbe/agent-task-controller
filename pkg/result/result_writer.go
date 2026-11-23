// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	notifcore "github.com/bborbe/notification"
	notifcmd "github.com/bborbe/notification/command/notification"
	libtime "github.com/bborbe/time"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

//counterfeiter:generate -o ../../mocks/result_writer.go --fake-name ResultWriter . ResultWriter

// ResultWriter writes a Task back to the vault task file.
type ResultWriter interface {
	WriteResult(ctx context.Context, req lib.Task) error
}

// NewResultWriter creates a ResultWriter that locates task files in the vault
// and writes the result, committing via gitClient. vaultName is the controller's
// VAULT_NAME, used to heal legacy task files that lack a target_vault stamp.
// resolver is the scanner's identifier→path index, consulted before the vault walk;
// a nil resolver selects the walk-only behaviour and is a test-only value — no
// production call site passes nil.
func NewResultWriter(
	client vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
	waiter libtime.WaiterDuration,
	notificationSender notifcmd.NotificationPublishCommandSender,
	resolver TaskPathResolver,
	humans HumanAssignees,
	opts ...ResultWriterOption,
) ResultWriter {
	w := &resultWriter{
		client:             client,
		taskDir:            taskDir,
		vaultName:          vaultName,
		currentDateTime:    currentDateTime,
		metrics:            m,
		waiter:             waiter,
		notificationSender: notificationSender,
		resolver:           resolver,
		humanAssignees:     humans,

		escalationPublishedAt: make(map[string]libtime.DateTime),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// KeptStatePublisher republishes a task's on-disk state on the task event topic.
// publisher.TaskPublisher satisfies it.
type KeptStatePublisher interface {
	PublishChanged(ctx context.Context, task lib.Task) error
}

// ResultWriterOption configures optional ResultWriter behaviour.
type ResultWriterOption func(*resultWriter)

// WithKeptStatePublisher makes the writer republish the task's on-disk state
// whenever the ownership guard rejected an incoming field.
//
// Without it, a result the guard reduces to a no-op produces no commit
// (AtomicReadModifyWriteAndCommitPush skips an unchanged file), so the vault
// scanner sees no change and no event is published. An executor whose view
// holds the rejected values then never learns they were rejected, and re-drives
// the task once its success grace lapses. Measured 2026-10-28 and 2026-10-31 on
// sun-k3s: completed, parked pr-override records re-spawned and re-approved
// already-merged PRs.
func WithKeptStatePublisher(p KeptStatePublisher) ResultWriterOption {
	return func(w *resultWriter) {
		w.keptStatePublisher = p
	}
}

type resultWriter struct {
	// keptStatePublisher, when set, republishes the on-disk state after a write in
	// which the ownership guard rejected at least one incoming field.
	keptStatePublisher KeptStatePublisher

	client             vaultapi.Client
	taskDir            string
	vaultName          string
	currentDateTime    libtime.CurrentDateTimeGetter
	metrics            metrics.Metrics
	waiter             libtime.WaiterDuration
	notificationSender notifcmd.NotificationPublishCommandSender
	resolver           TaskPathResolver
	// humanAssignees names the assignee values that are human owners, so the
	// human_review chokepoint retains them instead of clearing them. Empty (the
	// unconfigured default) clears every assignee, as before.
	humanAssignees HumanAssignees

	// escalationCoalescingMutex guards escalationPublishedAt. The check and the record in
	// claimEscalationCoalescingSlot are one atomic step under it, so two concurrent
	// escalations of one key cannot both publish. In-process only, never serialized: a
	// restart clears the window.
	escalationCoalescingMutex sync.Mutex
	// escalationPublishedAt records the time of the last *published* ping per coalescing
	// key — never the time of the last escalation, so a suppressed escalation does not
	// extend the window. Pruned on every claim, so its size is bounded by the distinct
	// keys seen in the last escalationCoalescingWindow.
	escalationPublishedAt map[string]libtime.DateTime
}

// escalation records one assignee-clear observed while building a write.
//
// It is *recorded* inside the modify closure, which AtomicReadModifyWriteAndCommitPush
// re-runs on every git retry, but *published* only after the commit succeeds — so a
// retry re-records the same escalation without ever double-publishing. The recorder is
// set-only: a later attempt that re-reads the now-empty on-disk assignee must not erase
// the escalation an earlier attempt genuinely performed.
type escalation struct {
	taskIdentifier   string
	taskName         string
	previousAssignee string
	status           string
	phase            string
}

// taskNameFromRelPath derives the operator-facing task name from the task file's
// vault-relative path. Personal-vault tasks carry no title in frontmatter — the
// filename IS the title — so the basename minus the extension is the name.
func taskNameFromRelPath(relPath string) string {
	return strings.TrimSuffix(filepath.Base(relPath), ".md")
}

// redirectorBaseURL is the public HTTPS endpoint that converts a URL Telegram
// will carry into an obsidian:// deeplink.
//
// The deeplink cannot be attached to the message directly: Telegram accepts only
// http/https (and tg://) in a text_link entity URL and answers any other scheme
// with `400 ... entity URL ... is invalid: Unsupported URL protocol`, which fails
// the whole message rather than degrading the link to plain text. So the message
// carries an https:// URL and the redirector emits the custom scheme on the tap.
// The client follows that 302 out of the in-app browser and hands off to Obsidian
// — verified by tapping a throwaway redirect from the phone before this was built.
const redirectorBaseURL = "https://redirect.benjamin-borbe.de/obsidian"

// vaultDeeplink renders a link to the task file, so the escalation message is
// one click from the notification into the parked task.
//
// The URL is the redirector's, not obsidian://: see redirectorBaseURL for why the
// raw scheme cannot be carried by the message.
//
// url.QueryEscape is form encoding: it renders a space as "+". The redirector
// re-emits the value into an Obsidian URI, and Obsidian's URI handler does not
// decode "+" as a space, so a path carrying literal "+" characters matches no
// file. Every "+" in the escaped result is therefore rewritten to "%20" — the
// encoding the vault's own convention documents. The redirector absorbs a "+"
// as well, so this rewrite is belt-and-braces rather than the only defence.
//
// The rewrite runs on the escaped result, never on the input: escaping has already
// turned a literal "+" in a task name into "%2B", so no genuine character can be
// rewritten, and one post-step covers both query values at once.
//
// vaultName and relPath are raw, unescaped values. A caller passing an
// already-escaped value would have it escaped a second time.
func vaultDeeplink(vaultName, relPath string) string {
	return strings.ReplaceAll(
		fmt.Sprintf(
			"%s?vault=%s&file=%s",
			redirectorBaseURL,
			url.QueryEscape(vaultName),
			url.QueryEscape(strings.TrimSuffix(relPath, ".md")),
		),
		"+",
		"%20",
	)
}

// publishEscalation emits one agent-escalation notification through the shared
// core. It deliberately sets no Target: the deployed routing table owns the
// channel decision (Discord + Telegram today, additive handlers later), which is
// the point of publishing into the core rather than calling a channel directly.
//
// A publish failure never fails the write. The result is already committed, and
// losing the ping is strictly better than rolling back a task write because a
// notification broker was briefly unavailable.
func (r *resultWriter) publishEscalation(ctx context.Context, e escalation) {
	if r.notificationSender == nil {
		return
	}
	// Fail-open: a task name the frozen pattern does not match publishes exactly as it
	// does today and creates no window state, so a format change degrades to one ping per
	// file and never to silence.
	coalescingKey, coalescable := escalationCoalescingKey(e.taskName)
	claimed := false
	if coalescable {
		if !r.claimEscalationCoalescingSlot(coalescingKey, r.currentDateTime.Now()) {
			// V(1): the only signal separating a coalesced repeat from a delivered ping.
			// Both deployed controllers run -v=2, so this stays visible in the pod logs.
			glog.V(1).Infof(
				"escalation notification coalesced for key %s, task %s",
				coalescingKey,
				e.taskName,
			)
			return
		}
		claimed = true
	}
	relPath := filepath.Join(r.taskDir, e.taskName+".md")
	// Built once: the same URI goes into the message body and into the V(1) audit
	// line below, so computing it twice would both duplicate the work and let the
	// two copies drift apart.
	deeplink := vaultDeeplink(r.vaultName, relPath)
	message := fmt.Sprintf(
		"escalation: %s cleared its assignee — status %s, phase %s\n%s",
		e.previousAssignee,
		e.status,
		e.phase,
		deeplink,
	)
	command := notifcmd.NotificationPublishCommand{
		Type:    notifcore.AgentEscalationNotificationType,
		Message: notifcore.NotificationMessage(message),
		Metadata: map[string]string{
			"taskIdentifier":   e.taskIdentifier,
			"taskName":         e.taskName,
			"previousAssignee": e.previousAssignee,
		},
	}
	if err := r.notificationSender.SendPublishNotificationCommand(ctx, command); err != nil {
		if claimed {
			r.releaseEscalationCoalescingSlot(coalescingKey)
		}
		glog.Warningf(
			"publish agent-escalation notification for task %s (%s) escalated by %s failed: %v",
			e.taskIdentifier,
			e.taskName,
			e.previousAssignee,
			err,
		)
		return
	}
	// V(1): a per-publish audit trail, not a default-operator signal — the push
	// itself is what reaches the operator. Both deployed controllers run -v=2, so
	// this stays visible in the pod logs the proof reads.
	glog.V(1).Infof(
		"assignee cleared → notification published for task %s (%s) escalated by %s deeplink %s",
		e.taskName,
		e.taskIdentifier,
		e.previousAssignee,
		deeplink,
	)
}

// claimEscalationCoalescingSlot reports whether an escalation of key may publish, and
// records now as that key's last published ping when it may. The check and the record are
// one atomic step under the mutex. The window is pruned here, so the map holds only keys
// seen inside the last escalationCoalescingWindow. Expiry is read from the injected clock,
// never time.Now(), so the boundary is testable without sleeping.
func (r *resultWriter) claimEscalationCoalescingSlot(key string, now libtime.DateTime) bool {
	r.escalationCoalescingMutex.Lock()
	defer r.escalationCoalescingMutex.Unlock()
	for existingKey, publishedAt := range r.escalationPublishedAt {
		if now.Sub(publishedAt) >= escalationCoalescingWindow {
			delete(r.escalationPublishedAt, existingKey)
		}
	}
	if _, published := r.escalationPublishedAt[key]; published {
		return false
	}
	r.escalationPublishedAt[key] = now
	return true
}

// releaseEscalationCoalescingSlot releases a claim taken by claimEscalationCoalescingSlot
// when the sender rejected the command, so a broker outage is followed by the next
// escalation for that key publishing normally. The entry it deletes was outside the window
// it replaced (the claim overwrote it only because its window had elapsed), so deleting it
// leaves the map in the same state as no record at all.
func (r *resultWriter) releaseEscalationCoalescingSlot(key string) {
	r.escalationCoalescingMutex.Lock()
	defer r.escalationCoalescingMutex.Unlock()
	delete(r.escalationPublishedAt, key)
}

// notFoundAttempts is the total number of times WriteResult looks for the task file
// before giving up, and notFoundBackoff is the pause between those attempts.
//
// A miss is not always permanent: the controller lists files over git-rest's HTTP API
// rather than a local clone, and git-rest pulls on a timer, so a result can arrive
// before the file it belongs to is visible. Without a retry that result is dropped for
// good — WriteResult acks the Kafka message either way.
//
// Retrying in-process rather than by returning an error is deliberate. The consumer runs
// SkipCorruptBatches:false on an offset consumer, so a message that keeps failing is
// never skipped; returning an error for a permanently-missing file (deleted or renamed
// task) would block the partition and halt the controller. An in-process retry bounds the
// cost at notFoundBackoff*(notFoundAttempts-1) per miss and always lets the offset advance.
const (
	notFoundAttempts = 3
	notFoundBackoff  = libtime.Duration(time.Second)
)

// escalationCoalescingWindow is the fixed window inside which a repeat escalation of one
// PR publishes nothing. Frozen at 30 minutes: derived from the measured same-key gap
// distribution (median 18.2 minutes; 534 of 877 consecutive same-key creations inside 30
// minutes) and matching the operator's reported 8-11 duplicate pings a day. There is no
// config surface — a tunable window has no named consumer, and a switch that disables the
// coalescing re-opens the flood this closes.
const escalationCoalescingWindow = 30 * libtime.Minute

// escalationTaskNamePattern is the frozen, anchored parse that derives the coalescing key
// from a task name. The format is owned by github-pr-watcher (computeTaskTitle, built in
// pkg/filename.go, with appendRetryToken folding " - retry-<taskid[:8]>" into the suffix):
// "PR <kind> <provider> - <owner>-<repo> - <number> - <shortSHA>[- <slug>][- <suffix>]".
// Only the owner-qualified repo token and the PR number are captured, because everything
// after the short SHA — the title slug, the retry suffix, the task kind word and the
// provider word — varies between retries of one PR while those two do not. Anchored at the
// start with every captured group a space-delimited token, so neither " - " nor a newline
// in a crafted PR title can reach a captured value.
var escalationTaskNamePattern = regexp.MustCompile(`^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`)

// escalationCoalescingKey derives the coalescing key for a task name: the owner-qualified
// repo token and the PR number, joined as "<owner>-<repo>#<number>". Reports false for a
// task name the frozen pattern does not match, and the caller then publishes uncoalesced —
// fail-open, so a format change degrades to one ping per file and never to silence. A
// matched name always yields a non-empty token and a numeric number: \S+ requires at least
// one non-space byte and [0-9]+ at least one digit, and the match is anchored at the start,
// so a flood of arbitrary names cannot grow the window map.
func escalationCoalescingKey(taskName string) (string, bool) {
	match := escalationTaskNamePattern.FindStringSubmatch(taskName)
	if match == nil {
		return "", false
	}
	return match[1] + "#" + match[2], true
}

// resolveFromIndex consults the resolver for one identifier. It reports hit=false —
// meaning "run the walk" — for a nil resolver, for a miss, and for a hit whose single
// read or frontmatter parse failed or whose file no longer carries the identifier; the
// snapshot is rebuilt every scan cycle, so a stale entry lives at most one cycle and
// the walk is the correct recovery. A resolver error is returned as an error and never
// downgraded to a miss: an ambiguous identifier reported as a miss would be re-derived
// by the walk, which could pick one of the two files — the 2026-08-31 incident.
//
// A hit reads exactly one file and issues no directory listing. The path is the one
// the resolver observed; it is never built from id.
func resolveFromIndex(
	ctx context.Context,
	client vaultapi.Client,
	taskDir string,
	id lib.TaskIdentifier,
	resolver TaskPathResolver,
) (string, lib.TaskFrontmatter, bool, error) {
	if resolver == nil {
		return "", nil, false, nil
	}
	relPath, found, resolveErr := resolver.Resolve(ctx, id)
	if resolveErr != nil {
		return "", nil, false, resolveErr
	}
	if !found {
		glog.V(2).Infof("FindTaskFilePath: index miss for task %s, falling back to walk", id)
		return "", nil, false, nil
	}
	task, readErr := client.ReadTask(ctx, string(id))
	if readErr != nil {
		glog.V(2).
			Infof("FindTaskFilePath: index hit %s unreadable (%v), falling back to walk", relPath, readErr)
		return "", nil, false, nil
	}
	// ⚠️ The path this returns becomes the WRITE ADDRESS (WriteResult derives the
	// task address from it), so it must come from the task just read — not from the
	// resolver's cached entry. The index is rebuilt once per cycle; a task renamed
	// since then still resolves here by identifier, and returning its OLD title
	// would address a different task (or none) — the 2026-08-31 wrong-task write.
	existingFrontmatter := lib.TaskFrontmatter(task.Frontmatter)
	livePath := taskRelPathFor(taskDir, *task)
	glog.V(2).Infof("FindTaskFilePath: index hit for task %s at %s", id, livePath)
	return livePath, existingFrontmatter, true, nil
}

// taskRelPathFor composes the vault-relative path a task's title implies. It is a
// KEY for logging and for the identifier index, never a write address.
func taskRelPathFor(taskDir string, task vaultapi.Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		return ""
	}
	return filepath.Join(taskDir, title+".md")
}

// FindTaskFilePath returns the relative path of the task whose frontmatter has
// task_identifier == id or a non-empty task_identifier_previous == id, plus the
// parsed existing frontmatter.
//
// It consults resolver first: on a hit it reads exactly that one task — no listing
// — and on a miss, on a nil resolver, and on a hit whose read fails, it falls back
// to listing the vault's tasks. A resolver error is returned as-is and does not
// trigger the listing.
//
// The fallback matches CANDIDATES in the listing — which carries both the current
// identifier and the recorded predecessor — and reads only those, so a miss costs a
// handful of reads instead of one per listed task.
//
// Returns ("", nil, nil) when no match is found (not an error).
// Returns ("", nil, err) naming both paths when more than one task carries id: the
// match is ambiguous, so no caller may write.
func FindTaskFilePath(
	ctx context.Context,
	client vaultapi.Client,
	taskDir string,
	id lib.TaskIdentifier,
	resolver TaskPathResolver,
) (string, lib.TaskFrontmatter, error) {
	indexPath, indexFrontmatter, hit, resolveErr := resolveFromIndex(
		ctx, client, taskDir, id, resolver,
	)
	if resolveErr != nil {
		return "", nil, resolveErr
	}
	if hit {
		return indexPath, indexFrontmatter, nil
	}
	tasks, err := client.ListTasks(ctx, vaultapi.Filter{})
	if err != nil {
		return "", nil, errors.Wrapf(ctx, err, "list tasks")
	}
	// The listing carries both keys — the current identifier as ID and the recorded
	// predecessor as TaskIdentifierPrevious — so the match runs in memory and only
	// the candidates are read. Reading every listed task is what made an index miss
	// cost 86.2 s and 102.4 s at the shipped concurrency: one ReadTask per listed
	// task, ~13 000 of them on the personal vault, at ~64 ms each.
	//
	// The candidates are still READ before they are accepted, because the listing's
	// ID is task_identifier OR a slug of the filename for a page carrying none —
	// matching the raw listing field alone would begin resolving slug-shaped
	// requests to tasks that never claimed them. The read restores the exact
	// frontmatter match the old walk performed, at one read per candidate (usually
	// zero or one) instead of one per listed task.
	//
	// The match rules are unchanged and are still evaluated in listing order, so a
	// duplicate error names the same two paths it always did.
	candidates := make([]vaultapi.Task, 0, 2)
	for _, listed := range tasks {
		if listedCouldCarryIdentifier(listed, id) {
			candidates = append(candidates, listed)
		}
	}

	var matchedRelPath string
	var matchedByPrevious bool
	var existingFrontmatter lib.TaskFrontmatter
	reads := 0
	for _, listed := range candidates {
		res := readCandidateTask(ctx, client, taskDir, id, listed)
		if res.err != nil {
			return "", nil, res.err
		}
		if res.read {
			reads++
		}
		if !res.matched {
			continue
		}
		// Two tasks sharing one identifier — whether by the current key or the
		// recorded predecessor — is never resolvable: picking either one writes an
		// agent's result onto a task that may belong to a different one. The old
		// behaviour silently kept the LAST match, which on 2026-08-31 marked a task
		// done carrying another task's analysis. Fail loudly instead.
		if matchedRelPath != "" {
			return "", nil, errors.Errorf(
				ctx,
				"duplicate task_identifier %s in %s and %s",
				id, matchedRelPath, res.relPath,
			)
		}
		matchedRelPath = res.relPath
		matchedByPrevious = res.viaPrevious
		logTaskFileMatch(res.relPath, id, res.viaPrevious)
		existingFrontmatter = res.frontmatter
	}
	// The ratio a measurement reads: how many pages this resolution actually read
	// against how many the listing offered. V(2) because the personal controller runs
	// at logLevel 2, so a V(3) line would never be emitted on the pod.
	glog.V(2).Infof(
		"FindTaskFilePath: index miss for task %s — reads=%d candidates=%d listing=%d",
		id, reads, len(candidates), len(tasks),
	)
	if !matchedByPrevious {
		recordWalkMatch(ctx, resolver, id, matchedRelPath)
	}
	return matchedRelPath, existingFrontmatter, nil
}

// listedCouldCarryIdentifier reports whether one listing row could carry id under
// either key. It is a CANDIDATE filter, not the match: the caller reads the file and
// applies taskIdentifierMatch to its frontmatter, because the listing's ID falls back
// to a filename slug for a page that carries no task_identifier.
func listedCouldCarryIdentifier(listed vaultapi.Task, id lib.TaskIdentifier) bool {
	if lib.TaskIdentifier(listed.ID) == id {
		return true
	}
	return listed.TaskIdentifierPrevious != "" &&
		lib.TaskIdentifier(listed.TaskIdentifierPrevious) == id
}

// candidateResult is one candidate's verdict, or the error that stopped it. A read
// that misses is neither an error nor a match — the vault moved between the listing
// and the read — while any other read error is carried so the caller fails rather
// than silently skipping a task it could not inspect. read reports whether a
// ReadTask was actually issued, so the caller can count them for the ratio log.
type candidateResult struct {
	relPath     string
	viaPrevious bool
	matched     bool
	read        bool
	frontmatter lib.TaskFrontmatter
	err         error
}

// readCandidateTask reads one listing candidate and reports whether it matches id.
func readCandidateTask(
	ctx context.Context,
	client vaultapi.Client,
	taskDir string,
	id lib.TaskIdentifier,
	listed vaultapi.Task,
) candidateResult {
	if err := ctx.Err(); err != nil {
		return candidateResult{err: errors.Wrapf(ctx, err, "read candidate")}
	}
	// The listing is a flat projection and carries no frontmatter, so each candidate's
	// frontmatter has to be read. An empty title has no address to read, exactly as
	// typedFileOps.list skips it.
	relPath := taskRelPathFor(taskDir, listed)
	if relPath == "" {
		return candidateResult{}
	}
	task, readErr := client.ReadTask(ctx, taskAddressFromRelPath(relPath))
	if readErr != nil {
		if errors.Is(readErr, vaultapi.ErrNotFound) {
			return candidateResult{read: true}
		}
		return candidateResult{read: true, err: errors.Wrapf(ctx, readErr, "read task %s", relPath)}
	}
	// The read task's title is the address a caller can write to, so compose the
	// match path from it; an empty title has no address and cannot match.
	relPath = taskRelPathFor(taskDir, *task)
	if relPath == "" {
		return candidateResult{read: true}
	}
	current, _ := task.Frontmatter["task_identifier"].(string)
	previous, _ := task.Frontmatter["task_identifier_previous"].(string)
	matched, viaPrevious := taskIdentifierMatch(current, previous, id)
	if !matched {
		return candidateResult{read: true}
	}
	return candidateResult{
		relPath:     relPath,
		viaPrevious: viaPrevious,
		matched:     true,
		read:        true,
		frontmatter: lib.TaskFrontmatter(task.Frontmatter),
	}
}

// taskIdentifierMatch reports whether current or previous names id, and whether
// the match came through the recorded predecessor. The predecessor is only
// consulted when non-empty, so an absent key never matches an empty request.
func taskIdentifierMatch(current, previous string, id lib.TaskIdentifier) (bool, bool) {
	if lib.TaskIdentifier(current) == id {
		return true, false
	}
	if previous != "" && lib.TaskIdentifier(previous) == id {
		return true, true
	}
	return false, false
}

// logTaskFileMatch logs a walk match at V(2), distinguishing a match through
// the recorded predecessor from one through the current identifier.
func logTaskFileMatch(relPath string, id lib.TaskIdentifier, viaPrevious bool) {
	if viaPrevious {
		glog.V(2).Infof(
			"FindTaskFilePath: matched file %s for task %s via task_identifier_previous",
			relPath, id,
		)
		return
	}
	glog.V(2).Infof("FindTaskFilePath: matched file %s for task %s", relPath, id)
}

// recordWalkMatch records a unique walk match in the identifier→path index. The walk
// just proved relPath is the only file carrying id, so the next lookup for id is one
// read, not another full walk. A miss and a nil resolver record nothing.
func recordWalkMatch(
	ctx context.Context,
	resolver TaskPathResolver,
	id lib.TaskIdentifier,
	relPath string,
) {
	if relPath == "" || resolver == nil {
		return
	}
	resolver.Record(ctx, id, relPath)
}

func (r *resultWriter) WriteResult(ctx context.Context, req lib.Task) error {
	glog.V(2).Infof("WriteResult: starting for task %s", req.TaskIdentifier)
	glog.V(3).Infof("WriteResult: scanning taskDir=%s", r.taskDir)

	var matchedRelPath string
	for attempt := 1; attempt <= notFoundAttempts; attempt++ {
		var err error
		matchedRelPath, _, err = FindTaskFilePath(
			ctx,
			r.client,
			r.taskDir,
			req.TaskIdentifier,
			r.resolver,
		)
		if err != nil {
			return errors.Wrapf(ctx, err, "find task file path failed")
		}
		if matchedRelPath != "" {
			if attempt > 1 {
				// Unconditional: this is the only signal separating a transient
				// git-rest lag miss from a permanent one, and the `not_found`
				// alert threshold is tuned from that split.
				glog.Infof(
					"task file for identifier %s resolved on attempt %d of %d",
					req.TaskIdentifier,
					attempt,
					notFoundAttempts,
				)
			}
			break
		}
		if attempt == notFoundAttempts {
			break
		}
		glog.V(2).Infof(
			"task file not found for identifier %s on attempt %d of %d, retrying in %v",
			req.TaskIdentifier,
			attempt,
			notFoundAttempts,
			notFoundBackoff,
		)
		if waitErr := r.waiter.Wait(ctx, notFoundBackoff); waitErr != nil {
			return errors.Wrapf(ctx, waitErr, "wait before retrying task file lookup")
		}
	}

	if matchedRelPath == "" {
		// A result whose frontmatter carries no target_vault reached every controller:
		// routing.ShouldProcessResult falls through to true precisely so the owning
		// vault gets a chance to find and heal the file. The non-owning controllers
		// then miss by construction, and counting that as `not_found` reports routine
		// fan-out as data loss — it is what made AgentControllerResultNotFound fire on
		// normal fleet traffic. Count it as `unowned` instead and keep `not_found` for
		// a miss on a result this controller was actually routed.
		if _, stamped := req.Frontmatter.String("target_vault"); !stamped {
			glog.V(2).Infof(
				"task file not found for unstamped identifier %s after %d attempts, "+
					"skipping (no target_vault: another vault likely owns it)",
				req.TaskIdentifier,
				notFoundAttempts,
			)
			r.metrics.ResultsWrittenTotal("unowned").Inc()
			return nil
		}
		glog.Warningf(
			"task file not found for identifier %s after %d attempts, skipping",
			req.TaskIdentifier,
			notFoundAttempts,
		)
		r.metrics.ResultsWrittenTotal("not_found").Inc()
		return nil
	}

	// The matched path is a title-derived key, and the API resolves a title, so the
	// same address works whether the match came through the current identifier or
	// the recorded predecessor. No absolute path is composed: there is no clone.
	taskAddress := taskAddressFromRelPath(matchedRelPath)
	glog.V(2).Infof("WriteResult: writing for task %s", req.TaskIdentifier)
	writeStart := r.currentDateTime.Now()
	if err := r.writeAndPublish(ctx, taskAddress, req, matchedRelPath); err != nil {
		r.metrics.ResultsWrittenTotal("error").Inc()
		return errors.Wrapf(ctx, err, "read-modify-write failed")
	}

	glog.V(2).Infof("WriteResult: completed successfully for task %s in %dms",
		req.TaskIdentifier, r.currentDateTime.Now().Time().Sub(writeStart.Time()).Milliseconds())
	r.metrics.ResultsWrittenTotal("success").Inc()
	return nil
}

// writeAndPublish commits the merged result and, when that write escalated the task,
// publishes one agent-escalation notification through the shared core.
//
// The publish lives here — after the commit — rather than inside the modify closure
// because that closure re-runs on every git retry: a publish hooked into the
// clearAssignee chokepoint would ping once per attempt, not once per escalation.
// The recorder is set-only for the same reason: a later attempt that re-reads the
// now-empty on-disk assignee must not erase an escalation an earlier attempt
// genuinely performed.
func (r *resultWriter) writeAndPublish(
	ctx context.Context,
	taskAddress string,
	req lib.Task,
	matchedRelPath string,
) error {
	var escalated *escalation
	var kept *lib.Task
	modify := r.buildResultModifyFn(
		ctx,
		req,
		func(e escalation) { escalated = &e },
		func(t *lib.Task) { kept = t },
	)
	// The caller-side read-modify-write loop the port keeps deliberately: a PATCH
	// can be retried exactly as a commit could, and the modify closure is the one
	// place the merge rules live. The escalation recorder stays set-only for the
	// same reason it always was — an attempt that re-reads a now-empty assignee must
	// not erase an escalation an earlier attempt performed.
	var lastErr error
	for attempt := 1; attempt <= resultWriteAttempts; attempt++ {
		current, readErr := r.client.ReadTask(ctx, taskAddress)
		if readErr != nil {
			return errors.Wrapf(ctx, readErr, "read task %s", taskAddress)
		}
		rendered, renderErr := renderTaskForWrite(current)
		if renderErr != nil {
			return errors.Wrapf(ctx, renderErr, "render task %s", taskAddress)
		}
		next, modifyErr := modify(rendered)
		if modifyErr != nil {
			return modifyErr
		}
		if bytes.Equal(next, rendered) {
			// The closure decided nothing changes: no write, no retry.
			lastErr = nil
			break
		}
		nextFields, nextBody, splitErr := splitRenderedForWrite(ctx, next)
		if splitErr != nil {
			return errors.Wrapf(ctx, splitErr, "split task %s", taskAddress)
		}
		fields, excluded := changedFieldsForWrite(current.Frontmatter, nextFields)
		if len(excluded) > 0 {
			// V(0): the drop is rare and load-bearing. Gating it behind -v=2 would
			// make it invisible on a controller running at the default verbosity,
			// which is where the "why did my result not land" question gets asked.
			glog.Infof(
				"WriteResult: excluded=[%s] task=%s — PATCH-immutable keys never ride the write",
				strings.Join(excluded, ","),
				taskAddress,
			)
		}
		if _, patchErr := r.client.PatchTaskWithBody(
			ctx, current.ID, fields, nextBody,
		); patchErr != nil {
			lastErr = patchErr
			glog.V(2).Infof(
				"WriteResult: patch attempt %d of %d failed for %s: %v",
				attempt, resultWriteAttempts, taskAddress, patchErr,
			)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return errors.Wrapf(
			ctx,
			lastErr,
			"patch task %s after %d attempts",
			taskAddress,
			resultWriteAttempts,
		)
	}
	if escalated != nil {
		escalated.taskName = taskNameFromRelPath(matchedRelPath)
		r.publishEscalation(ctx, *escalated)
	}
	if kept != nil {
		r.publishKeptState(ctx, *kept)
	}
	return nil
}

// resultWriteAttempts bounds the caller-side read-modify-write loop. It mirrors
// the git-retry budget the old atomic write had: enough to survive a transient
// conflict, few enough that a persistent failure surfaces quickly.
const resultWriteAttempts = 3

// renderTaskForWrite writes a task back as the frontmatter block plus body the
// modify closure expects, so the closure stays the single home of the merge rules.
func renderTaskForWrite(task *vaultapi.Task) ([]byte, error) {
	frontmatter := task.Frontmatter
	if frontmatter == nil {
		frontmatter = map[string]any{}
	}
	rendered, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(rendered) + "---\n" + task.Body), nil
}

// splitRenderedForWrite parses a rendered task back into fields and body.
func splitRenderedForWrite(
	ctx context.Context,
	content []byte,
) (map[string]any, string, error) {
	frontmatter, err := ExtractFrontmatter(ctx, content)
	if err != nil {
		return nil, "", err
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &fields); err != nil {
		return nil, "", err
	}
	body, err := ExtractBody(ctx, content)
	if err != nil {
		return nil, "", err
	}
	return fields, body, nil
}

// changedFieldsForWrite returns the keys whose value differs, which is what a
// typed PATCH wants: it replaces only the keys it is given. It also returns the
// keys it dropped because the vault API's task-PATCH rule does not admit them, so
// the caller can log the exclusion — the drop is otherwise silent and
// indistinguishable from "nothing changed".
//
// The drop follows the vault API's own rule, lib.TaskPatchFieldAdmitted, imported
// from github.com/bborbe/agent, rather than a list kept here: the API and this
// writer share one definition, so the writer cannot fall one field behind the
// schema. The failure this removes is severe: the API refuses the WHOLE request on
// the first key it does not admit, so a single refused key vetoes every mutable key
// in the same write. That is how an in-flight result was dropped after a re-address
// (measured on dev 2026-11-19: the payload still carried the superseded identifier,
// the patch named it, and three attempts all answered 400 unknown_field).
func changedFieldsForWrite(current, next map[string]any) (map[string]any, []string) {
	fields := make(map[string]any)
	var excluded []string
	for key, value := range next {
		existing, onDisk := current[key]
		unchanged := onDisk && reflect.DeepEqual(existing, value)
		if !lib.TaskPatchFieldAdmitted(key) {
			if !unchanged {
				excluded = append(excluded, key)
			}
			continue
		}
		if unchanged {
			continue
		}
		fields[key] = value
	}
	sort.Strings(excluded)
	return fields, excluded
}

// taskAddressFromRelPath is the address the API resolves for a task path: its
// title. The API keys identifier, name and slug in one space, so a result that
// arrived under a repaired identifier still reaches its task.
func taskAddressFromRelPath(relPath string) string {
	return strings.TrimSuffix(filepath.Base(relPath), ".md")
}

// publishKeptState republishes the state the guard kept on disk, so every
// executor's view converges on it. Best-effort: the vault write already
// succeeded, so a publish failure is logged rather than failing the result.
func (r *resultWriter) publishKeptState(ctx context.Context, task lib.Task) {
	if r.keptStatePublisher == nil {
		return
	}
	if err := r.keptStatePublisher.PublishChanged(ctx, task); err != nil {
		glog.Warningf(
			"event=kept_state_publish_failed task=%s err=%v",
			task.TaskIdentifier,
			err,
		)
		return
	}
	glog.Infof("event=kept_state_published task=%s", task.TaskIdentifier)
}

// buildResultModifyFn returns a modify callback for AtomicReadModifyWriteAndCommitPush
// that re-reads the on-disk frontmatter+body on every git retry, then applies the
// merge / retry-counter / cap rules. Re-reading per retry eliminates the read-then-write
// race against the partial-update executor (task_update_frontmatter_executor.go) that
// previously caused stale-snapshot writes to roll back terminal status.
func (r *resultWriter) buildResultModifyFn(
	ctx context.Context,
	req lib.Task,
	recordEscalation func(escalation),
	recordKeptState func(*lib.Task),
) func(current []byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		frontmatterStr, err := ExtractFrontmatter(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract frontmatter")
		}
		bodyStr, err := ExtractBody(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract body")
		}
		var currentOnDisk lib.TaskFrontmatter
		// NOTE: inlined yaml ops mirror the pre-race version of WriteResult; pkg/command/ helpers
		// are unexported and the lift-and-share is out of scope for this race fix.
		if err := yaml.Unmarshal([]byte(frontmatterStr), &currentOnDisk); err != nil {
			return nil, errors.Wrapf(ctx, err, "unmarshal current frontmatter")
		}

		merged, decisions := MergeFrontmatter(currentOnDisk, req.Frontmatter)
		for _, d := range decisions {
			glog.Infof(
				"ownership guard kept on-disk: task %s field %s kept %v rejected %v",
				req.TaskIdentifier,
				d.Field,
				d.Kept,
				d.Rejected,
			)
		}
		// Heal-on-write: a legacy task file created before target_vault stamping
		// permanently stops falling through to both controllers once the owning
		// controller writes it. Never overrides an existing target_vault.
		HealTargetVault(merged, r.vaultName)
		ClearCurrentJobIfTerminal(merged)
		mergedBody := mergeBody(bodyStr, string(req.Content))
		body := r.applyRetryCounter(merged, currentOnDisk, mergedBody)

		// Detect the escalation here rather than hooking the chokepoint: all four
		// escalation rows funnel through applyRetryCounter (-> applyTriggerCap /
		// ClearAssigneeIfHumanReview / applyRetryCap), so one comparison covers every
		// row without a publish call per row. This closure re-runs on each git retry,
		// which is exactly why the publish itself lives in WriteResult, after the
		// commit — see the escalation type's doc comment.
		if priorAssignee := string(currentOnDisk.Assignee()); priorAssignee != "" &&
			string(merged.Assignee()) == "" {
			escalatedPhase := ""
			if p := merged.Phase(); p != nil {
				escalatedPhase = string(*p)
			}
			recordEscalation(escalation{
				taskIdentifier:   string(req.TaskIdentifier),
				previousAssignee: priorAssignee,
				status:           string(merged.Status()),
				phase:            escalatedPhase,
			})
		}

		marshaledFrontmatter, err := yaml.Marshal(map[string]any(merged))
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "marshal frontmatter")
		}
		// merged is what lands on disk (or already is, when the write is a no-op),
		// so it is the state every view must converge on. Overwritten on every
		// attempt — nil when this attempt rejected nothing — so only the attempt
		// that committed decides the publish, which runs once after the commit.
		var kept *lib.Task
		if len(decisions) > 0 {
			kept = &lib.Task{
				Object:         req.Object,
				TaskIdentifier: req.TaskIdentifier,
				Frontmatter:    merged,
				Content:        lib.TaskContent(body),
			}
		}
		recordKeptState(kept)
		return []byte("---\n" + string(marshaledFrontmatter) + "---\n" + body), nil
	}
}

func (r *resultWriter) applyRetryCounter(merged, existing lib.TaskFrontmatter, body string) string {
	if isTerminalStatus(merged.Status()) {
		return body
	}

	// Trigger-count cap enforcement runs unconditionally before the retry path: it is
	// a derived invariant on the on-disk state that must hold after every WriteResult.
	// The triggerCount > 0 guard prevents degenerate escalation of brand-new tasks
	// where trigger_count is absent.
	triggerCount := merged.TriggerCount()
	body = r.applyTriggerCap(merged, existing, triggerCount, body)

	// human_review assignee-clear guard runs before the retry-cap path. On a
	// pr-reviewer agent's first post-spawn write, the merged frontmatter carries
	// spawn_notification: true (inherited from the executor's spawn-time
	// UpdateFrontmatterCommand) AND incoming phase: human_review (from
	// Result.NextPhase via resolveNextPhase). The guard fires regardless of
	// spawn_notification state — it used to sit above the spawn_notification early
	// return, which no longer exists. Same bug class as prompt 075 (2026-04-24
	// applyTriggerCap precedent, task ba1bad61); spec 041 fixes the 2026-05-25 prod
	// incident (task bborbe-agent #3).
	// needs_input: agent explicitly requested human review — clear assignee so task surfaces in operator inbox.
	// Routes through ClearAssigneeIfHumanReview (spec 042) so the partial-update executor shares the same chokepoint.
	ClearAssigneeIfHumanReview(merged, r.humanAssignees)

	// There is deliberately NO spawn_notification short-circuit here.
	//
	// The old branch early-returned on merged.SpawnNotification() and deleted the key
	// so it would fire exactly once; because merged is marshalled verbatim, that
	// delete stripped the marker from the task file on the first result write after a
	// spawn (measured 2026-11-12, task 561f36f9, commit abb64beeeb).
	//
	// Keeping the key and keeping the short-circuit are mutually exclusive with a
	// boolean marker — the value persists on disk, so any test of it is permanently
	// true and disables applyRetryCap. The branch is gone and applyRetryCap always
	// runs. Rationale, the rejected incoming-keyed variant and the measurements:
	// docs/controller-design.md § Retry cap.
	//
	// ⚠️ Do not reintroduce it. A one-shot spawn marker must be a field the controller
	// clears without losing it from the record — not this boolean.

	// retry_count is retired — no deployed component writes it any more, so it is no
	// longer controller-owned and an incoming value is applied rather than discarded.
	// The cap therefore reads the counter from the ON-DISK frontmatter, not from the
	// merge: the merge is incoming-wins now, so a stale echo carrying retry_count: 0
	// would otherwise suppress the escalation a legacy record had already earned.
	// Reading the disk value keeps the cap's meaning exactly what it was before the
	// retirement, when the ownership guard pinned the same value.
	retryCount := existing.RetryCount()
	body = r.applyRetryCap(merged, existing, retryCount, body)

	return body
}

// TriggerCapEngaged reports whether the trigger cap applies to this frontmatter.
//
// The cap is engaged when max_triggers is explicitly set OR a ref is present. The
// ref arm mirrors the executor's refusal predicate (checkTriggerBudget in
// agent-task-executor), which caps a ref-bearing task at MaxTriggers() — the lib
// default of 3 — even when max_triggers is absent. Without the ref arm a
// ref-bearing task is refused by the executor and never escalated by either write
// site, so it parks silently at its lifecycle phase with a live assignee and no
// job: the silent-parking defect this predicate closes.
func TriggerCapEngaged(fm lib.TaskFrontmatter) bool {
	if _, ok := fm["max_triggers"]; ok {
		return true
	}
	_, hasRef := fm.String("ref")
	return hasRef
}

// applyTriggerCap enforces the trigger-count cap. The cap is engaged for an
// explicit max_triggers or a ref-bearing task (see TriggerCapEngaged); a plain
// recurring task with neither is never escalated and never has its routing
// assignee stripped, so it keeps re-dispatching (the lib default-3 fallback would
// kill the loop). The triggerCount > 0 guard prevents degenerate escalation of
// brand-new tasks where trigger_count is absent. When the task is already parked
// (section present), existing.Phase() restores the on-disk lifecycle phase to
// prevent stale-result phase clobber (cap stickiness).
func (r *resultWriter) applyTriggerCap(
	merged, existing lib.TaskFrontmatter,
	triggerCount int,
	body string,
) string {
	if !TriggerCapEngaged(merged) {
		return body
	}
	if triggerCount == 0 || triggerCount < merged.MaxTriggers() {
		return body
	}
	agentName := r.clearAssigneeUnlessHuman(merged)
	// Park the record. Clearing the assignee alone is not durable: the executor
	// restores the routing value on its next reconcile tick and the scanner then
	// re-arms the budget, so the row returns to the queue as a fresh candidate.
	// `hold` is refused by the executor's own status filter, which is what makes the
	// escalation survive a re-dispatch. See CappedEscalatedStatus.
	//
	// Written as a plain string, not the typed constant: TaskFrontmatter.Status()
	// reads the map entry with a `.(string)` assertion, so a domain.TaskStatus in
	// the map reads back as "" and silently un-parks the record.
	merged["status"] = string(CappedEscalatedStatus)
	if ContainsTriggerCapEscalation(body) {
		restoreExistingPhase(existing, merged)
		return CollapseTriggerCapEscalation(body)
	}
	return body + r.triggerEscalationSection(triggerCount, agentName, merged)
}

// applyRetryCap enforces the retry-count cap for records that still carry the counter.
// The cap is opt-in: an absent max_retries means no cap, so a record is never escalated
// and never has its routing assignee stripped (the lib default-3 fallback would kill the
// re-dispatch loop). retry_count is retired — no deployed component increments it — so
// this applies only to legacy records that already carry a counter beside an explicit
// max_retries; the caller passes the on-disk value, never the merged one. When the task
// is already parked (section present), existing.Phase() restores the on-disk lifecycle
// phase (cap stickiness).
func (r *resultWriter) applyRetryCap(
	merged, existing lib.TaskFrontmatter,
	retryCount int,
	body string,
) string {
	if _, ok := merged["max_retries"]; !ok {
		return body
	}
	if retryCount < merged.MaxRetries() {
		return body
	}
	agentName := r.clearAssigneeUnlessHuman(merged)
	if containsEscalationSection(body, "## Retry Escalation") {
		restoreExistingPhase(existing, merged)
		return body
	}
	return body + r.escalationSection(retryCount, agentName)
}

// clearAssigneeUnlessHuman is the cap-path counterpart of ClearAssigneeIfHumanReview:
// it captures the current assignee for the escalation body, but leaves a configured
// human owner in place.
//
// The cap paths need the same exception for the same reason. They run inside the same
// result write as the human_review chokepoint — applyTriggerCap before it and
// applyRetryCap after it — so without this a human-owned record whose trigger or retry
// cap is engaged loses its owner on that write, silently undoing the chokepoint's
// retention. A cap escalation exists to park a runaway *agent* task in the operator
// inbox; a record a human already owns is already in a queue, so clearing the owner
// removes it from that queue without putting it anywhere else.
//
// Returns the name the escalation body should report: the human's own, unchanged.
func (r *resultWriter) clearAssigneeUnlessHuman(merged lib.TaskFrontmatter) string {
	return ClearAssigneeUnlessHuman(merged, r.humanAssignees)
}

// ClearAssigneeUnlessHuman is the exported form of clearAssigneeUnlessHuman for
// callers outside this package — today the update-frontmatter executor's own
// trigger-cap branch, which clears before its human-aware chokepoint runs and so
// cannot rely on that chokepoint to protect the owner.
//
// Same contract as the method: capture the current assignee for the escalation
// body, clear it unless it names a configured human owner, and return the name
// the body should report.
func ClearAssigneeUnlessHuman(merged lib.TaskFrontmatter, humans HumanAssignees) string {
	assignee := string(merged.Assignee())
	if humans.Contains(assignee) {
		return assignee
	}
	return clearAssignee(merged)
}

// clearAssignee sets previous_assignee to the current assignee value (if non-empty),
// then clears assignee to "". Returns the captured name for use in escalation body text.
// This is the single chokepoint for all assignee-clear operations in the result writer.
func clearAssignee(merged lib.TaskFrontmatter) string {
	agentName := string(merged.Assignee())
	if agentName != "" {
		merged["previous_assignee"] = agentName
	}
	merged["assignee"] = ""
	return agentName
}

// ClearAssignee is the exported form of clearAssignee for callers outside this
// package (the increment executor). It preserves the exact semantics — capture the
// current assignee into previous_assignee when non-empty, then clear assignee — so
// the two escalation write sites produce an identical parked state.
func ClearAssignee(merged lib.TaskFrontmatter) string {
	return clearAssignee(merged)
}

// ClearAssigneeIfHumanReview enforces the spec-039 / spec-042 doctrine:
// when merged frontmatter has phase == "human_review", clear assignee via
// clearAssignee (which captures the prior assignee into previous_assignee
// if non-empty, then sets assignee to ""). Returns the prior assignee
// name (empty string when no clear happened OR when the prior assignee
// was already empty). This is the single chokepoint for the human_review
// assignee-clear invariant; both the result writer and the partial-update
// executor (spec 042) route through here.
//
// humans is the configured set of assignee values that name a human owner (see
// HumanAssignees). A record already carrying one of them is left untouched: the
// doctrine's purpose is that an *agent* must not keep a record it parked, and
// clearing a human's owner instead removes the record from that human's queue —
// the exact failure the routing stamp writes the owner to prevent. Everything not
// in the set is treated as an agent and cleared, so an empty set reproduces the
// pre-existing behaviour exactly. The set is supplied by configuration because this
// package cannot distinguish a human name from an agent name on its own.
func ClearAssigneeIfHumanReview(merged lib.TaskFrontmatter, humans HumanAssignees) string {
	phase, ok := merged["phase"].(string)
	if !ok || phase != "human_review" {
		return ""
	}
	if assignee, isString := merged["assignee"].(string); isString && humans.Contains(assignee) {
		return ""
	}
	return clearAssignee(merged)
}

// restoreExistingPhase writes the on-disk phase back into merged when the existing
// frontmatter has a phase value. Used to enforce cap stickiness on repeated writes.
func restoreExistingPhase(existing, merged lib.TaskFrontmatter) {
	if p := existing.Phase(); p != nil {
		merged["phase"] = string(*p)
	}
}

func (r *resultWriter) escalationSection(retryCount int, agentName string) string {
	ts := r.currentDateTime.Now().UTC().Format(time.RFC3339)
	return fmt.Sprintf(
		"\n## Retry Escalation\n\n- **Timestamp:** %s\n- **Attempts:** %d\n- **Assignee:** %s\n- **Last error:** see agent output above\n",
		ts,
		retryCount,
		agentName,
	)
}

func (r *resultWriter) triggerEscalationSection(
	triggerCount int,
	agentName string,
	merged lib.TaskFrontmatter,
) string {
	return TriggerCapEscalationSection(
		triggerCount,
		merged.MaxTriggers(),
		agentName,
		r.currentDateTime.Now(),
	)
}

// TriggerCapEscalationSection renders the ## Trigger Cap Escalation body section
// following the established precedent. Exported so the increment executor — the
// only site the deadline-failure path reaches — writes the same section the result
// writer does, rather than a second, drifting copy.
func TriggerCapEscalationSection(
	triggerCount, maxTriggers int,
	agentName string,
	now libtime.DateTime,
) string {
	return fmt.Sprintf(
		"\n## Trigger Cap Escalation\n\n- **Timestamp:** %s\n- **Trigger count:** %d\n- **Max triggers:** %d\n- **Assignee:** %s\n- **Last agent output:** see `## Result` above\n",
		now.UTC().Format(time.RFC3339),
		triggerCount,
		maxTriggers,
		agentName,
	)
}

// containsEscalationSection reports whether body already has the given
// escalation header on its own line. Used to prevent duplicate escalation
// sections when WriteResult runs multiple times on a task that is already
// at cap (e.g. agent publishes another result while the task sits in
// phase: human_review).
func containsEscalationSection(body, header string) bool {
	return strings.Contains(body, "\n"+header+"\n")
}

// ContainsTriggerCapEscalation reports whether body already carries the
// ## Trigger Cap Escalation section. Exported so the increment executor shares the
// duplicate guard without re-spelling the heading.
func ContainsTriggerCapEscalation(body string) bool {
	return containsEscalationSection(body, "## Trigger Cap Escalation")
}

// bodySection is one markdown heading and the content that follows it.
type bodySection struct {
	heading string // the full heading line, e.g. "## Result\n" (line ending included)
	content string // everything after the heading line up to the next heading line or end of body
}

// splitBody splits a markdown body into a preamble (text before the first "## "
// heading line) and an ordered list of sections. A heading line is a line
// starting with the exact prefix "## " (hash-hash-space). A bare "---" line is
// never a heading. Both "\n" and "\r\n" line endings are tolerated: a trailing
// "\r" before "\n" is part of the terminator, so the heading name of the line
// "## Parked\r\n" is "## Parked". Returns ("", nil) for an empty body.
func splitBody(body string) (preamble string, sections []bodySection) {
	if body == "" {
		return "", nil
	}
	var preambleBuilder strings.Builder
	type sectionAcc struct {
		heading string
		content strings.Builder
	}
	var accs []sectionAcc
	current := -1
	for len(body) > 0 {
		var line string
		if idx := strings.IndexByte(body, '\n'); idx >= 0 {
			line = body[:idx+1]
			body = body[idx+1:]
		} else {
			line = body
			body = ""
		}
		if _, isHeading := headingName(line); isHeading {
			accs = append(accs, sectionAcc{heading: line})
			current = len(accs) - 1
			continue
		}
		if current < 0 {
			preambleBuilder.WriteString(line)
		} else {
			accs[current].content.WriteString(line)
		}
	}
	sections = make([]bodySection, 0, len(accs))
	for _, a := range accs {
		sections = append(sections, bodySection{heading: a.heading, content: a.content.String()})
	}
	return preambleBuilder.String(), sections
}

// headingName returns the heading name — the heading line with its terminator
// ("\n", "\r\n", or nothing) removed — and whether the line is a heading, i.e.
// starts with the exact prefix "## ".
func headingName(line string) (string, bool) {
	name := strings.TrimSuffix(line, "\r\n")
	name = strings.TrimSuffix(name, "\n")
	if !strings.HasPrefix(name, "## ") {
		return "", false
	}
	return name, true
}

// mergeBody merges the on-disk body with the incoming body by heading: an
// on-disk-only heading is preserved in place with its content, a heading
// present in both is replaced in place by the incoming content, a heading
// present only in the incoming body is appended after the last on-disk section,
// and the preamble follows the DB-4 rule (incoming preamble wins when it has
// text; otherwise the on-disk preamble is preserved). Preserved sections keep
// their original line endings verbatim.
func mergeBody(existingBody, incomingBody string) string {
	existingPreamble, existingSections := splitBody(existingBody)
	incomingPreamble, incomingSections := splitBody(incomingBody)

	incomingByName := make(map[string]bodySection, len(incomingSections))
	incomingOrder := make([]string, 0, len(incomingSections))
	for _, sec := range incomingSections {
		name, _ := headingName(sec.heading)
		if _, seen := incomingByName[name]; !seen {
			incomingOrder = append(incomingOrder, name)
		}
		incomingByName[name] = sec
	}

	// Preamble: incoming wins when it has any non-whitespace text; otherwise the
	// on-disk preamble is preserved (e.g. when the incoming body starts with a
	// heading and carries no preamble of its own).
	resultPreamble := existingPreamble
	if strings.TrimSpace(incomingPreamble) != "" {
		resultPreamble = incomingPreamble
	}

	existingNames := make(map[string]bool, len(existingSections))
	var out strings.Builder
	out.WriteString(resultPreamble)
	for _, sec := range existingSections {
		name, _ := headingName(sec.heading)
		existingNames[name] = true
		if incoming, ok := incomingByName[name]; ok {
			// Same-named heading: the fresh incoming content lands in place.
			out.WriteString(incoming.heading)
			out.WriteString(incoming.content)
		} else {
			// On-disk-only heading: preserved verbatim with its content.
			out.WriteString(sec.heading)
			out.WriteString(sec.content)
		}
	}
	// Append, in incoming order, every incoming section whose heading name was
	// not present among the existing sections.
	for _, name := range incomingOrder {
		if existingNames[name] {
			continue
		}
		sec := incomingByName[name]
		out.WriteString(sec.heading)
		out.WriteString(sec.content)
	}
	return out.String()
}

// GuardDecision records a single ownership-guard discard: the merge kept the
// on-disk value of a guarded field (Kept is nil when the field was absent on disk
// and stays absent) and rejected a differing incoming value. Equal values produce
// no decision.
type GuardDecision struct {
	Field    string
	Kept     any
	Rejected any
}

// controllerOwnedFields lists frontmatter keys whose on-disk value always wins:
// the result writer never lets the agent's incoming value overwrite them, and an
// incoming value can never introduce a controller-owned key that is absent on disk.
//
// trigger_scope belongs here with trigger_count: the executor charges one spawn by
// writing BOTH (triggerBudgetWrite's !hasScope arm), so a stale snapshot that drops
// trigger_scope does not merely lose a label — it makes the next spawn take the
// scope-write arm again and re-derive the count, which is how the budget drifts.
//
// max_triggers belongs here because it is the cap's DEFINITION, not a measurement:
// the cap predicate reads it, so a snapshot that raises it in the same write
// suppresses the escalation. Nothing but a task's own create sets it.
var controllerOwnedFields = []string{
	"trigger_count",
	"trigger_scope",
	"max_triggers",
}

// operatorOwnedFields lists frontmatter keys that are the operator's routing
// surface: the result writer keeps the on-disk value over a differing incoming
// snapshot (the agent authors these keys from its spawn-time snapshot, not live
// state), but — unlike controllerOwnedFields — an incoming value may introduce
// an operator-owned key that is absent on disk (a spawn/claim names an assignee
// on a task that never carried one). Two exceptions, both for "assignee" only and
// both producing no guard decision because each applies the incoming value rather
// than discarding it: an incoming empty string is always applied — the deliverer's
// deliberate Failed/needs_input clear (spec 039), never a stale snapshot — and a
// fresh reassignment declaration (reassignmentDeclarationKey) applies the incoming
// non-empty assignee, because the emitter states the change is deliberate rather
// than an echo.
var operatorOwnedFields = []string{
	"assignee",
	"previous_assignee",
}

// operatorPinnedFields lists frontmatter keys that only the operator may write.
// The agent's result payload is built from the TASK_CONTENT snapshot injected
// at spawn, so any value it carries for these keys is an echo of how the task
// looked before the run, never a decision the agent made. The on-disk value
// therefore always wins — and, unlike operatorOwnedFields, an incoming value can
// never introduce a pinned key that is absent on disk: an operator who removed
// the key while the job ran must not see a late result write put it back.
//
// cluster is the operator's routing pin (which controller publishes the task).
// The operator is its only legitimate writer; the automatic cluster assigner is
// being retired. Observed live 2026-10-21: a pr-reviewer job spawned from a
// snapshot carrying cluster: sun, the operator removed cluster: six minutes
// later, and the job's result write restored cluster: sun — 27 tasks were
// re-tagged onto a cluster that was being shut down.
//
// Unlike controllerOwnedFields, a discarded incoming value is reported as a
// guard decision even when the key is absent on disk (Kept: nil), so every
// dropped echo shows up in the ownership-guard log line.
var operatorPinnedFields = []string{
	"cluster",
}

// reassignmentDeclarationKey names the frontmatter key an emitter sets to declare
// that the assignee in this write is a deliberate reassignment, not an echo of the
// emitter's spawn-time snapshot. The rule is needed because the guard cannot tell
// those apart from the values alone: an agent re-routing a task and an agent echoing
// a stale assignee both arrive as a differing incoming value.
//
// The key is CONSUMED, not persisted: it is deleted from the merged frontmatter
// once honoured, so it never lands on the task. That is what makes a declaration
// unambiguous. Keeping it on disk would make a *repeat* handoff indistinguishable
// from an echo — a task re-delegated to the analyzer and re-analysed would write
// the same value the earlier handoff left behind, and a value-differs test would
// discard it, re-creating the exact stranding this key exists to fix. Consumed,
// the task file never carries the key, so an agent's spawn-time snapshot can never
// echo one: a declaration in the incoming payload is always this run's deliberate
// write.
//
// The spelling is a frozen cross-repo contract with the emitter (the sentry-fix
// handoff sets it to its own step name). A renamed emitter key silently reverts
// every reassignment to a discard — the failure this key exists to fix, with no
// error anywhere.
const reassignmentDeclarationKey = "assignee_reassigned_by"

// accumulatedCounters lists frontmatter keys that are cumulative in the result
// write-back merge: when both the on-disk frontmatter and the incoming payload
// carry a numeric value for one of them, the written value is their sum. These
// keys are neither controller-owned (that guard would discard the emitted value,
// so the counter would never move) nor agent-owned (incoming-wins would reset a
// lifetime total to one run's value) — accumulation is the third ownership
// behaviour. The spellings are a frozen cross-repo contract with the agent-side
// emitter (spec 012): a renamed emitter key silently stops accumulating.
var accumulatedCounters = []string{
	"metrics_agent_turns",
	"metrics_interaction_count",
}

// terminalStatuses lists the statuses that end a task's lifecycle. When the on-disk
// status is terminal, the merge pins it (discarding the incoming status) and the
// escalation machinery short-circuits. Terminal is decided via the normalizing
// TaskFrontmatter.Status() accessor compared against these constants.
var terminalStatuses = []domain.TaskStatus{
	domain.TaskStatusCompleted,
	domain.TaskStatusAborted,
}

// MergeFrontmatter returns a new frontmatter map with all keys from existing,
// overridden by all keys from incoming, then applies the field-ownership guard:
// controller-owned counters take the on-disk value when present on disk and are
// absent from the result when not; a terminal on-disk status is pinned;
// operator-pinned fields (cluster) always take the on-disk value, are absent from
// the result when absent on disk, and report every differing discarded incoming
// value as a decision (Kept nil when the key was absent on disk); and
// operator-owned routing fields (assignee, previous_assignee) take the on-disk
// value when present on disk, with two exceptions that apply the incoming assignee
// instead — an incoming empty string (the deliverer's clear) and a fresh
// reassignment declaration (reassignmentDeclarationKey); a discarded incoming
// assignee also discards the incoming task_type (the routing tuple travels
// together). The accumulated counters (metrics_agent_turns,
// metrics_interaction_count) are summed when both sides carry a numeric value,
// keep the on-disk value when the incoming key is absent, and take the incoming
// value when the key is absent on disk; a non-numeric value on either side keeps
// the on-disk value verbatim and is reported as a decision when the two values
// differ. The returned decisions name every field whose differing incoming value
// was discarded (equal values produce no decision).
// Neither input map is modified.
func MergeFrontmatter(
	existing, incoming lib.TaskFrontmatter,
) (lib.TaskFrontmatter, []GuardDecision) {
	merged := make(lib.TaskFrontmatter, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		merged[k] = v
	}
	var decisions []GuardDecision

	// Controller-owned counters: the on-disk value always wins when the key exists
	// on disk (kept verbatim, whatever its type); a key absent on disk stays absent
	// even if the incoming payload carries it.
	for _, field := range controllerOwnedFields {
		diskValue, onDisk := existing[field]
		incomingValue, inIncoming := incoming[field]
		if !onDisk {
			if inIncoming {
				delete(merged, field)
			}
			continue
		}
		merged[field] = diskValue
		if inIncoming && !FrontmatterValueEqual(diskValue, incomingValue) {
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
			)
		}
	}

	// Terminal on-disk status is pinned: the on-disk status value is kept and the
	// incoming status is discarded. Terminal is decided by the normalizing Status()
	// accessor compared against terminalStatuses, never by raw string equality on
	// the unparsed value. The status decision is reported only when the normalized
	// values differ (an incoming alias such as "done" against on-disk "completed"
	// is normalized-equal and produces no decision).
	if diskStatus, onDisk := existing["status"]; onDisk && isTerminalStatus(existing.Status()) {
		merged["status"] = diskStatus
		if incomingStatus, ok := incoming["status"]; ok && existing.Status() != incoming.Status() {
			decisions = append(
				decisions,
				GuardDecision{Field: "status", Kept: diskStatus, Rejected: incomingStatus},
			)
		}
	}

	// Stale-planning pin: the result payload is built from the agent's spawn-time
	// snapshot, so a `phase` it carries may be an echo of how the task looked
	// before the run. The executor re-drives a task from `planning`, so a stale
	// `phase: planning` echo must never drag a task that has moved past it back
	// (docs/controller-design.md invariant). The on-disk phase is kept and the
	// discarded echo reported. Every other phase move stays agent-owned, and a
	// declared backward move (the sentry deep→fix handoff re-arms execution →
	// planning and says so) is applied.
	decisions = applyPhaseMonotonic(existing, incoming, merged, decisions)

	// Operator-pinned fields (cluster): the on-disk value always wins and an
	// incoming value can never introduce the key when it is absent on disk.
	decisions = applyOperatorPinnedFields(existing, incoming, merged, decisions)

	// Operator-owned routing fields (assignee, previous_assignee): the on-disk
	// value always wins over a differing incoming snapshot, with two exceptions that
	// apply the incoming assignee — an incoming empty string (the deliverer's clear)
	// and a fresh reassignment declaration.
	decisions = applyOperatorOwnedFields(existing, incoming, merged, decisions)
	decisions = applyAccumulatedCounters(existing, incoming, merged, decisions)
	return merged, decisions
}

// applyPhaseMonotonic pins the on-disk phase against a stale `planning` echo.
// The result payload is built from the agent's spawn-time snapshot, so a `phase`
// it carries may describe the task before the run. The executor re-drives a task
// from `planning`, so a payload still carrying `phase: planning` must never drag
// a task that has moved past planning back into a fresh planning run
// (docs/controller-design.md: "a stale payload phase must never re-drive a task
// that has moved on"). When the incoming phase is `planning` and the on-disk
// phase is any other phase, the on-disk phase is kept and the discarded echo is
// reported as a decision. An absent or empty incoming phase never erases an
// on-disk phase. Every other phase move stays agent-owned — the field-ownership
// contract lets an agent advance the phase and move it among non-planning phases
// — and a payload carrying the reassignment declaration applies its phase
// verbatim (a deliberate backward move, e.g. the sentry deep→fix handoff).
func applyPhaseMonotonic(
	existing, incoming, merged lib.TaskFrontmatter,
	decisions []GuardDecision,
) []GuardDecision {
	incomingValue, inIncoming := incoming["phase"]
	if !inIncoming {
		return decisions // base merge already kept the on-disk phase
	}
	if declaresReassignment(incoming) {
		return decisions // deliberate backward move
	}
	existingPhase := existing.Phase()
	if existingPhase == nil {
		return decisions // nothing on disk to protect
	}
	incomingPhase := incoming.Phase()
	if incomingPhase == nil {
		// An empty phase in the payload must not erase the on-disk phase.
		merged["phase"] = existing["phase"]
		return append(decisions, GuardDecision{
			Field:    "phase",
			Kept:     existing["phase"],
			Rejected: incomingValue,
		})
	}
	if *incomingPhase != domain.TaskPhasePlanning ||
		*existingPhase == domain.TaskPhasePlanning {
		return decisions // not a stale planning echo: agent owns the phase
	}
	merged["phase"] = existing["phase"]
	return append(decisions, GuardDecision{
		Field:    "phase",
		Kept:     existing["phase"],
		Rejected: incomingValue,
	})
}

// applyOperatorPinnedFields applies the operator-pinned rule to the merged
// frontmatter: the on-disk value of each operatorPinnedFields key always wins.
// A key present on disk keeps its on-disk value and a differing incoming value is
// reported as a decision. A key absent on disk stays absent even when incoming
// carries it, and the discarded incoming value is reported as a decision with
// Kept: nil. Equal values and an incoming payload without the key produce no
// decision. Returns the decision list with any discards appended.
func applyOperatorPinnedFields(
	existing, incoming, merged lib.TaskFrontmatter,
	decisions []GuardDecision,
) []GuardDecision {
	for _, field := range operatorPinnedFields {
		diskValue, onDisk := existing[field]
		incomingValue, inIncoming := incoming[field]
		if !inIncoming {
			continue // base merge already kept the on-disk value (or its absence)
		}
		if !onDisk {
			delete(merged, field)
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: nil, Rejected: incomingValue},
			)
			continue
		}
		merged[field] = diskValue
		if !FrontmatterValueEqual(diskValue, incomingValue) {
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
			)
		}
	}
	return decisions
}

// applyOperatorOwnedFields applies the operator-owned routing-field rule to the
// merged frontmatter. Assignee and previous_assignee are the operator's routing
// surface: when the key exists on disk, the on-disk value always wins over a
// differing incoming snapshot; unlike controller-owned counters, an incoming
// value may introduce a key that is absent on disk (the base merge already wrote
// it). Exception for "assignee" only: an incoming empty string is always applied
// (the deliverer's deliberate Failed/needs_input clear) and produces no
// decision. When the incoming assignee is discarded, the routing tuple's
// task_type is discarded with it (applyStaleRoutingTupleGuard). Returns the
// decision list with any discards appended.
func applyOperatorOwnedFields(
	existing, incoming, merged lib.TaskFrontmatter,
	decisions []GuardDecision,
) []GuardDecision {
	for _, field := range operatorOwnedFields {
		diskValue, onDisk := existing[field]
		incomingValue, inIncoming := incoming[field]
		if !onDisk {
			continue // incoming may introduce the key (already merged above)
		}
		if inIncoming && field == "assignee" {
			// The deliverer's empty-assignee clear is the string "". A non-string
			// incoming value (malformed payload) must not panic the merge — the
			// type-assert keeps this comparison panic-free, same doctrine as
			// FrontmatterValueEqual (spec 006).
			if incomingString, ok := incomingValue.(string); ok && incomingString == "" {
				merged[field] = incomingValue
				continue
			}
			// A reassignment declaration applies the incoming assignee: the emitter
			// states that this write deliberately re-routes the task, which is the
			// opposite of the stale spawn-time snapshot this guard discards. Applied,
			// not discarded — so no guard decision, same as the clear above. The
			// declaration is consumed with it: leaving it on disk would make a repeat
			// handoff on a re-driven task look like an echo, and be discarded.
			if declaresReassignment(incoming) {
				merged[field] = incomingValue
				delete(merged, reassignmentDeclarationKey)
				continue
			}
		}
		merged[field] = diskValue
		if inIncoming && !FrontmatterValueEqual(diskValue, incomingValue) {
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
			)
			if field == "assignee" {
				decisions = applyStaleRoutingTupleGuard(existing, incoming, merged, decisions)
			}
		}
	}
	return decisions
}

// applyStaleRoutingTupleGuard discards an incoming task_type when the
// operator-owned guard has just discarded the incoming assignee. assignee and
// task_type are the routing tuple: every deliberate reassignment (the sentry
// fix handoff and the triage→deep reassign) flips both together, and every
// agent result echoes both from its spawn-time snapshot. A payload whose
// assignee the guard rejected is therefore a stale snapshot, and its task_type
// is stale by construction — writing it reverts a later handoff's routing.
//
// Observed live 2026-10-18 (task 6ea12dd3, prod): the fix handoff wrote
// task_type: sentry-fix at 17:50:54; a superseded analyzer job's result merged
// at 18:16:42. The guard kept assignee sentry-fix-agent (and logged it) but
// task_type reverted to sentry-deep-analyzer, so the executor spawned
// sentry-fix-agent-…181717 with TaskType 'sentry-deep-analyzer' and the pod
// failed its analyzer preflight. The routing tuple must travel together.
//
// The legitimate same-assignee task_type change (the triage→deep reassign, which
// keeps the analyzer as assignee and only flips the task type) produces no
// assignee decision and is left untouched. Returns the decision list with a
// task_type discard appended when the on-disk and incoming values differ.
func applyStaleRoutingTupleGuard(
	existing, incoming, merged lib.TaskFrontmatter,
	decisions []GuardDecision,
) []GuardDecision {
	diskValue, onDisk := existing["task_type"]
	incomingValue, inIncoming := incoming["task_type"]
	if !onDisk || !inIncoming {
		return decisions
	}
	merged["task_type"] = diskValue
	if !FrontmatterValueEqual(diskValue, incomingValue) {
		decisions = append(
			decisions,
			GuardDecision{Field: "task_type", Kept: diskValue, Rejected: incomingValue},
		)
	}
	return decisions
}

// declaresReassignment reports whether the incoming payload declares a reassignment
// in this write. Presence is sufficient because the guard consumes the key — it
// never lands on disk, so an agent's spawn-time snapshot can never carry one and an
// incoming declaration is always this run's deliberate write. A non-string or empty
// declaration is treated as absent, which keeps the check panic-free on a malformed
// payload — same doctrine as FrontmatterValueEqual (spec 006).
func declaresReassignment(incoming lib.TaskFrontmatter) bool {
	declaration, ok := incoming[reassignmentDeclarationKey].(string)
	return ok && declaration != ""
}

// applyAccumulatedCounters applies the cumulative-counter rule to the merged
// frontmatter. When both sides carry a numeric value for one of the accumulated
// counters, the written value is their sum — compared and added by numeric value,
// so a JSON-decoded incoming float64 adds correctly to a YAML-decoded on-disk int —
// and NO guard decision is produced: accumulation is a transform, not a discard.
// When either side is non-numeric the on-disk value is kept verbatim and the
// incoming value is discarded, producing one decision naming the field when the two
// values differ and none when they are equal. A key absent on either side keeps the
// base merge's outcome: an absent incoming key leaves the on-disk value untouched,
// and an absent on-disk key takes the incoming value. Returns the decision list with
// any discards appended.
func applyAccumulatedCounters(
	existing, incoming, merged lib.TaskFrontmatter,
	decisions []GuardDecision,
) []GuardDecision {
	for _, field := range accumulatedCounters {
		diskValue, onDisk := existing[field]
		incomingValue, inIncoming := incoming[field]
		if !onDisk || !inIncoming {
			// The base merge already produced the right value: an absent incoming
			// key leaves the on-disk value untouched, and an absent on-disk key
			// takes the incoming value as the starting total.
			continue
		}
		diskNumber, diskIsNumber := numericValue(diskValue)
		incomingNumber, incomingIsNumber := numericValue(incomingValue)
		if diskIsNumber && incomingIsNumber {
			merged[field] = diskNumber + incomingNumber
			continue
		}
		merged[field] = diskValue
		if !FrontmatterValueEqual(diskValue, incomingValue) {
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
			)
		}
	}
	return decisions
}

// HealTargetVault stamps the controller's vaultName into frontmatter when the
// target_vault key is absent. An existing value (any value) is never overridden.
// This is the heal-on-write half of the frontmatter-command routing guard: a
// legacy task file created before target_vault stamping is written by whichever
// controller owns the write, and this stamp ensures the non-owning controller
// permanently stops falling through to that file (ShouldProcessResult and
// ShouldProcessFrontmatterCommand both return false for a mismatched stamp).
// Mutates fm in place; no-op when the key is already present.
func HealTargetVault(fm lib.TaskFrontmatter, vaultName string) {
	if _, ok := fm["target_vault"]; !ok {
		fm["target_vault"] = vaultName
	}
}

// ClearCurrentJobIfTerminal empties current_job when the merged status is
// terminal (completed / aborted). The executor clears the field only from
// process memory, so a run that finishes across an executor restart, or after
// its task was evicted from the executor's store, kept a stale current_job.
// A terminal record cannot own a live job, so clearing here cannot erase a
// newer job's name. Writes "" (the executor's own clear value). Mutates fm in
// place; no-op when the status is not terminal or the field is absent or empty.
func ClearCurrentJobIfTerminal(fm lib.TaskFrontmatter) {
	if !isTerminalStatus(fm.Status()) {
		return
	}
	if v, ok := fm["current_job"]; ok && v != nil && v != "" {
		fm["current_job"] = ""
	}
}

func isTerminalStatus(s domain.TaskStatus) bool {
	for _, t := range terminalStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// FrontmatterValueEqual reports whether two frontmatter values compare equal,
// treating numeric int/float64 pairs as equal so a JSON-decoded incoming counter
// (float64) equals a YAML-decoded on-disk counter (int) at steady state — equal
// counters must produce no guard log line (DB 6).
func FrontmatterValueEqual(a, b any) bool {
	// NEVER use `a == b` on two `any` values here. Go panics with
	// "comparing uncomparable type map[string]interface {}" when both sides hold
	// the same uncomparable dynamic type, and both sides come from YAML (on disk)
	// or JSON (incoming payload) decoding, either of which can yield a map or a
	// slice. A panic here would kill the single result-write chokepoint. The spec's
	// Failure Modes table requires a non-integer on-disk counter to be kept
	// verbatim, not to crash. reflect.DeepEqual never panics.
	af, aOK := numericValue(a)
	bf, bOK := numericValue(b)
	if aOK && bOK {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// ExtractFrontmatter returns the YAML frontmatter string between the opening and
// closing "---" delimiters. Returns an error if delimiters are missing.
func ExtractFrontmatter(ctx context.Context, content []byte) (string, error) {
	s := string(content)
	const delim = "---"
	if !strings.HasPrefix(s, delim) {
		return "", errors.Errorf(ctx, "no frontmatter delimiter found")
	}
	rest := strings.TrimPrefix(s, delim)
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	before, _, found := strings.Cut(rest, "\n---")
	if !found {
		return "", errors.Errorf(ctx, "frontmatter not closed")
	}
	return before, nil
}

// ExtractBody returns the file body — the bytes after the closing "---\n" delimiter.
// Returns an empty string if the body is empty; error if delimiters are missing.
func ExtractBody(ctx context.Context, content []byte) (string, error) {
	s := string(content)
	const delim = "---"
	if !strings.HasPrefix(s, delim) {
		return "", errors.Errorf(ctx, "no frontmatter delimiter found")
	}
	rest := strings.TrimPrefix(s, delim)
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	_, after, found := strings.Cut(rest, "\n---\n")
	if !found {
		return "", errors.Errorf(ctx, "frontmatter not closed")
	}
	return after, nil
}
