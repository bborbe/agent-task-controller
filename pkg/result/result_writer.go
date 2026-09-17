// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
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

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

//counterfeiter:generate -o ../../mocks/result_writer.go --fake-name ResultWriter . ResultWriter

// ResultWriter writes a Task back to the vault task file.
type ResultWriter interface {
	WriteResult(ctx context.Context, req lib.Task) error
}

// NewResultWriter creates a ResultWriter that locates task files in the vault
// and writes the result, committing via gitClient. vaultName is the controller's
// VAULT_NAME, used to heal legacy task files that lack a target_vault stamp.
func NewResultWriter(
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
	waiter libtime.WaiterDuration,
	notificationSender notifcmd.NotificationPublishCommandSender,
) ResultWriter {
	return &resultWriter{
		gitClient:          gitClient,
		taskDir:            taskDir,
		vaultName:          vaultName,
		currentDateTime:    currentDateTime,
		metrics:            m,
		waiter:             waiter,
		notificationSender: notificationSender,

		escalationPublishedAt: make(map[string]libtime.DateTime),
	}
}

type resultWriter struct {
	gitClient          gitclient.GitClient
	taskDir            string
	vaultName          string
	currentDateTime    libtime.CurrentDateTimeGetter
	metrics            metrics.Metrics
	waiter             libtime.WaiterDuration
	notificationSender notifcmd.NotificationPublishCommandSender

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

// vaultDeeplink renders an Obsidian URI for the task file, so the escalation
// message is one click from the notification into the parked task.
func vaultDeeplink(vaultName, relPath string) string {
	return fmt.Sprintf(
		"obsidian://open?vault=%s&file=%s",
		url.QueryEscape(vaultName),
		url.QueryEscape(strings.TrimSuffix(relPath, ".md")),
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
	message := fmt.Sprintf(
		"escalation: %s cleared its assignee — status %s, phase %s\n%s",
		e.previousAssignee,
		e.status,
		e.phase,
		vaultDeeplink(r.vaultName, relPath),
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
		"assignee cleared → notification published for task %s (%s) escalated by %s",
		e.taskName,
		e.taskIdentifier,
		e.previousAssignee,
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

// FindTaskFilePath lists files in taskDir via gitClient and returns the relative path of
// the .md file whose frontmatter has task_identifier == id, plus the parsed existing frontmatter.
// Returns ("", nil, nil) when no match is found (not an error).
// Returns ("", nil, err) naming both paths when more than one file carries id: the match is
// ambiguous, so no caller may write. Callers must check the error before treating an empty
// path as "not found" — the two outcomes mean opposite things.
func FindTaskFilePath(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	id lib.TaskIdentifier,
) (string, lib.TaskFrontmatter, error) {
	glob := taskDir + "/*.md"
	paths, err := gitClient.ListFiles(ctx, glob)
	if err != nil {
		return "", nil, errors.Wrapf(ctx, err, "list task files with glob %s", glob)
	}
	var matchedRelPath string
	var existingFrontmatter lib.TaskFrontmatter
	for _, relPath := range paths {
		content, readErr := gitClient.ReadFile(ctx, relPath)
		if readErr != nil {
			glog.V(3).Infof("FindTaskFilePath: skip %s (read error: %v)", relPath, readErr)
			continue
		}
		frontmatter, fmErr := ExtractFrontmatter(ctx, content)
		if fmErr != nil {
			glog.V(3).Infof("FindTaskFilePath: skip %s (frontmatter error: %v)", relPath, fmErr)
			continue
		}
		var fm struct {
			TaskIdentifier string `yaml:"task_identifier"`
		}
		if umErr := yaml.Unmarshal([]byte(frontmatter), &fm); umErr != nil {
			glog.V(3).Infof("FindTaskFilePath: skip %s (unmarshal error: %v)", relPath, umErr)
			continue
		}
		glog.V(3).
			Infof("FindTaskFilePath: file %s has task_identifier=%s", relPath, fm.TaskIdentifier)
		if lib.TaskIdentifier(fm.TaskIdentifier) == id {
			// Two files sharing one task_identifier is never resolvable: picking
			// either one writes an agent's result onto a file that may belong to a
			// different task. The previous behaviour silently kept the LAST match
			// from an unsorted ListFiles, which on 2026-08-31 marked
			// `Sentry Alert Fan-Out - 2026-08-31` done/completed carrying another
			// task's analysis, for a run no executor ever performed. Fail loudly
			// instead — the caller must not write anything.
			if matchedRelPath != "" {
				return "", nil, errors.Errorf(
					ctx,
					"duplicate task_identifier %s in %s and %s",
					id, matchedRelPath, relPath,
				)
			}
			matchedRelPath = relPath
			glog.V(2).Infof("FindTaskFilePath: matched file %s for task %s", matchedRelPath, id)
			var existingFm lib.TaskFrontmatter
			if umErr := yaml.Unmarshal([]byte(frontmatter), &existingFm); umErr != nil {
				glog.V(3).
					Infof("FindTaskFilePath: could not unmarshal existing frontmatter for %s: %v", relPath, umErr)
			} else {
				existingFrontmatter = existingFm
			}
		}
	}
	return matchedRelPath, existingFrontmatter, nil
}

func (r *resultWriter) WriteResult(ctx context.Context, req lib.Task) error {
	glog.V(2).Infof("WriteResult: starting for task %s", req.TaskIdentifier)
	glog.V(3).Infof("WriteResult: scanning taskDir=%s", r.taskDir)

	var matchedRelPath string
	for attempt := 1; attempt <= notFoundAttempts; attempt++ {
		var err error
		matchedRelPath, _, err = FindTaskFilePath(
			ctx,
			r.gitClient,
			r.taskDir,
			req.TaskIdentifier,
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

	absPath := filepath.Join(r.gitClient.Path(), matchedRelPath)
	commitMessage := fmt.Sprintf(
		"[agent-task-controller] write result for task %s",
		req.TaskIdentifier,
	)
	glog.V(2).Infof("WriteResult: writing and pushing for task %s", req.TaskIdentifier)
	if err := r.writeAndPublish(ctx, absPath, req, matchedRelPath, commitMessage); err != nil {
		r.metrics.ResultsWrittenTotal("error").Inc()
		return errors.Wrapf(ctx, err, "atomic read-modify-write and push failed")
	}

	glog.V(2).Infof("WriteResult: completed successfully for task %s", req.TaskIdentifier)
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
	absPath string,
	req lib.Task,
	matchedRelPath string,
	commitMessage string,
) error {
	var escalated *escalation
	if err := r.gitClient.AtomicReadModifyWriteAndCommitPush(
		ctx,
		absPath,
		r.buildResultModifyFn(ctx, req, func(e escalation) { escalated = &e }),
		commitMessage,
	); err != nil {
		return err
	}
	if escalated != nil {
		escalated.taskName = taskNameFromRelPath(matchedRelPath)
		r.publishEscalation(ctx, *escalated)
	}
	return nil
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
		return []byte("---\n" + string(marshaledFrontmatter) + "---\n" + body), nil
	}
}

func (r *resultWriter) applyRetryCounter(merged, existing lib.TaskFrontmatter, body string) string {
	if isTerminalStatus(merged.Status()) {
		return body
	}

	// Trigger-count cap enforcement runs unconditionally before any early
	// returns below: it is a derived invariant on the on-disk state that
	// must hold after every WriteResult. Placing it here also prevents the
	// spawn_notification short-circuit below from silently skipping
	// escalation on agent result writes that inherited spawn_notification
	// from a previous merge (observed live on dev 2026-04-24, task
	// ba1bad61: spawn-notification update set spawn_notification=true,
	// then the agent's result publish inherited it via mergeFrontmatter
	// and skipped the cap check, reverting phase: human_review to
	// ai_review). The triggerCount > 0 guard prevents degenerate
	// escalation of brand-new tasks where trigger_count is absent.
	triggerCount := merged.TriggerCount()
	body = r.applyTriggerCap(merged, existing, triggerCount, body)

	// human_review assignee-clear guard runs BEFORE the spawn_notification
	// early return below. On a pr-reviewer agent's first post-spawn write, the merged
	// frontmatter carries spawn_notification: true (inherited from the executor's
	// spawn-time UpdateFrontmatterCommand) AND incoming phase: human_review (from
	// Result.NextPhase via resolveNextPhase). If this guard were below the
	// spawn_notification early return, clearAssignee would never fire and the
	// operator inbox filter (assignee == "") would miss the task. Same bug class
	// as prompt 075 (2026-04-24 applyTriggerCap precedent, task ba1bad61); spec 041
	// fixes the 2026-05-25 prod incident (task bborbe-agent #3).
	// needs_input: agent explicitly requested human review — clear assignee so task surfaces in operator inbox.
	// Routes through ClearAssigneeIfHumanReview (spec 042) so the partial-update executor shares the same chokepoint.
	ClearAssigneeIfHumanReview(merged)

	if merged.SpawnNotification() {
		delete(merged, "spawn_notification")
		return body
	}

	// retry_count is authoritative in the task file — the executor bumped it
	// at spawn time (spec 011). The writer only applies escalation.
	retryCount := merged.RetryCount()
	body = r.applyRetryCap(merged, existing, retryCount, body)

	return body
}

// applyTriggerCap enforces the trigger-count cap. The cap is opt-in: an absent
// max_triggers means no cap, so recurring tasks that accumulate trigger_count
// across re-dispatches are never escalated and never have their routing assignee
// stripped (the lib default-3 fallback would kill the re-dispatch loop). The
// triggerCount > 0 guard prevents degenerate escalation of brand-new tasks where
// trigger_count is absent. When the task is already parked (section present),
// existing.Phase() restores the on-disk lifecycle phase to prevent stale-result
// phase clobber (cap stickiness).
func (r *resultWriter) applyTriggerCap(
	merged, existing lib.TaskFrontmatter,
	triggerCount int,
	body string,
) string {
	if _, ok := merged["max_triggers"]; !ok {
		return body
	}
	if triggerCount == 0 || triggerCount < merged.MaxTriggers() {
		return body
	}
	agentName := clearAssignee(merged)
	if containsEscalationSection(body, "## Trigger Cap Escalation") {
		restoreExistingPhase(existing, merged)
		return body
	}
	return body + r.triggerEscalationSection(triggerCount, agentName, merged)
}

// applyRetryCap enforces the retry-count cap. The cap is opt-in: an absent max_retries
// means no cap, so tasks that accumulate retry_count across re-dispatches are never
// escalated and never have their routing assignee stripped (the lib default-3 fallback
// would kill the re-dispatch loop). When the task is already parked (section present),
// existing.Phase() restores the on-disk lifecycle phase (cap stickiness).
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
	agentName := clearAssignee(merged)
	if containsEscalationSection(body, "## Retry Escalation") {
		restoreExistingPhase(existing, merged)
		return body
	}
	return body + r.escalationSection(retryCount, agentName)
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

// ClearAssigneeIfHumanReview enforces the spec-039 / spec-042 doctrine:
// when merged frontmatter has phase == "human_review", clear assignee via
// clearAssignee (which captures the prior assignee into previous_assignee
// if non-empty, then sets assignee to ""). Returns the prior assignee
// name (empty string when no clear happened OR when the prior assignee
// was already empty). This is the single chokepoint for the human_review
// assignee-clear invariant; both the result writer and the partial-update
// executor (spec 042) route through here.
func ClearAssigneeIfHumanReview(merged lib.TaskFrontmatter) string {
	if phase, ok := merged["phase"].(string); ok && phase == "human_review" {
		return clearAssignee(merged)
	}
	return ""
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
	ts := r.currentDateTime.Now().UTC().Format(time.RFC3339)
	return fmt.Sprintf(
		"\n## Trigger Cap Escalation\n\n- **Timestamp:** %s\n- **Trigger count:** %d\n- **Max triggers:** %d\n- **Assignee:** %s\n- **Last agent output:** see `## Result` above\n",
		ts,
		triggerCount,
		merged.MaxTriggers(),
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
// on-disk value of a controller-owned field (or the pinned terminal status) and
// rejected a differing incoming value. Equal values produce no decision.
type GuardDecision struct {
	Field    string
	Kept     any
	Rejected any
}

// controllerOwnedFields lists frontmatter keys whose on-disk value always wins:
// the result writer never lets the agent's incoming value overwrite them, and an
// incoming value can never introduce a controller-owned key that is absent on disk.
var controllerOwnedFields = []string{
	"trigger_count",
	"retry_count",
}

// operatorOwnedFields lists frontmatter keys that are the operator's routing
// surface: the result writer keeps the on-disk value over a differing incoming
// snapshot (the agent authors these keys from its spawn-time snapshot, not live
// state), but — unlike controllerOwnedFields — an incoming value may introduce
// an operator-owned key that is absent on disk (a spawn/claim names an assignee
// on a task that never carried one). One exception, for "assignee" only: an
// incoming empty string is always applied — the deliverer's deliberate
// Failed/needs_input clear (spec 039), never a stale snapshot — and produces no
// guard decision.
var operatorOwnedFields = []string{
	"assignee",
	"previous_assignee",
}

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
// absent from the result when not; a terminal on-disk status is pinned; and
// operator-owned routing fields (assignee, previous_assignee) take the on-disk
// value when present on disk, with an incoming empty assignee always applied as
// the deliverer's clear exception. The accumulated counters (metrics_agent_turns,
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
		if inIncoming && !frontmatterValueEqual(diskValue, incomingValue) {
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

	// Operator-owned routing fields (assignee, previous_assignee): the on-disk
	// value always wins over a differing incoming snapshot, with an incoming empty
	// assignee always applied as the deliverer's clear exception.
	decisions = applyOperatorOwnedFields(existing, incoming, merged, decisions)
	decisions = applyAccumulatedCounters(existing, incoming, merged, decisions)
	return merged, decisions
}

// applyOperatorOwnedFields applies the operator-owned routing-field rule to the
// merged frontmatter. Assignee and previous_assignee are the operator's routing
// surface: when the key exists on disk, the on-disk value always wins over a
// differing incoming snapshot; unlike controller-owned counters, an incoming
// value may introduce a key that is absent on disk (the base merge already wrote
// it). Exception for "assignee" only: an incoming empty string is always applied
// (the deliverer's deliberate Failed/needs_input clear) and produces no
// decision. Returns the decision list with any discards appended.
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
			// frontmatterValueEqual (spec 006).
			if incomingString, ok := incomingValue.(string); ok && incomingString == "" {
				merged[field] = incomingValue
				continue
			}
		}
		merged[field] = diskValue
		if inIncoming && !frontmatterValueEqual(diskValue, incomingValue) {
			decisions = append(
				decisions,
				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
			)
		}
	}
	return decisions
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
		if !frontmatterValueEqual(diskValue, incomingValue) {
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

func isTerminalStatus(s domain.TaskStatus) bool {
	for _, t := range terminalStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// frontmatterValueEqual reports whether two frontmatter values compare equal,
// treating numeric int/float64 pairs as equal so a JSON-decoded incoming counter
// (float64) equals a YAML-decoded on-disk counter (int) at steady state — equal
// counters must produce no guard log line (DB 6).
func frontmatterValueEqual(a, b any) bool {
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
