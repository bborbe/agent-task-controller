// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/result"
)

// cappedIdleStatus is the only status a capped record is escalated from. A record
// that already reached a terminal status has nothing left to rescue, and escalating
// it would rewrite history for no gain.
const cappedIdleStatus = "in_progress"

// cappedEscalatedStatus is the status the sweep moves a rescued record to.
//
// Clearing the assignee alone is NOT durable, and this is the whole reason the sweep
// touches status at all. A task with an empty assignee whose task_type still resolves
// to a Config is treated by the executor as parked and gets its routing value written
// straight back (restoreParkedAssignee), and the scanner then reads that empty → named
// transition as an operator re-delegation and zeroes trigger_count — which re-arms the
// budget and the whole cycle repeats. Measured on nuke dev 2026-10-28: an escalated
// probe was re-assigned every ~60 s.
//
// `hold` breaks that loop at the executor's own eligibility filter: the default
// trigger statuses are `[in_progress]` only, so a held task is refused before the
// restore branch is ever reached, and it is simultaneously the human-owned state
// retry exhaustion is supposed to end in.
const cappedEscalatedStatus = "hold"

// isCappedIdle reports whether a task's frontmatter is at or past its trigger cap.
//
// This is the same predicate the executor refuses on (checkTriggerBudget in
// agent-task-executor) expressed through the controller's own helpers, so the sweep
// and the refusal cannot drift: TriggerCapEngaged covers the explicit max_triggers
// and the ref-bearing arm, and MaxTriggers falls back to the lib default of 3.
//
// A count of 0 is excluded on purpose. The result writer keeps the same guard for the
// same reason: a brand-new task that has never spawned must not be escalated
// degenerately just because its cap is small.
func isCappedIdle(fm lib.TaskFrontmatter) bool {
	if !result.TriggerCapEngaged(fm) {
		return false
	}
	count := fm.TriggerCount()
	return count > 0 && count >= fm.MaxTriggers()
}

// escalateCappedIdleTasks rescues tasks the executor refuses forever.
//
// The escalation shipped in v0.22.2/v0.22.3 fires from a *write*, and an idle capped
// record receives none: the executor's checkTriggerBudget writes nothing on the
// refusal path, so no spawn happens, so no job runs, so no result is published. Such
// a record sits at its lifecycle phase with a live assignee and no job indefinitely —
// measured on nuke prod 2026-10-28: five records skipped on every reconcile, and the
// controller never wrote to any of them.
//
// This sweep closes that hole from the side that can see it. It walks the scan
// index (in memory, no extra reads) and escalates every entry that is capped, still
// carries an assignee, and has no live job — the same parked state the result
// writer produces, so a rescued record is indistinguishable from one escalated by a
// result.
//
// Returns how many were escalated and whether any write failed. A failed write is
// reported rather than fatal: the next scan retries, and one unwritable file must not
// stop the others.
func (v *vaultScanner) escalateCappedIdleTasks(ctx context.Context) (int, bool) {
	escalated := 0
	writeError := false
	for relPath, entry := range v.hashes {
		if !entry.capped || entry.assignee == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return escalated, writeError
		default:
		}
		wrote, err := v.escalateCappedTask(ctx, relPath)
		if err != nil {
			glog.Warningf("escalateCappedIdle: %s: %v", relPath, err)
			writeError = true
			continue
		}
		if !wrote {
			continue
		}
		escalated++
		// Zero-hash sentinel so the next cycle re-processes the file and publishes
		// the change, with the cleared assignee recorded so the sweep does not
		// re-escalate it before that cycle runs.
		v.hashes[relPath] = fileEntry{
			hash:           [32]byte{},
			taskIdentifier: entry.taskIdentifier,
			assignee:       "",
		}
	}
	return escalated, writeError
}

// escalateCappedTask applies the escalation to one file through the atomic
// read-modify-write path, so it acts on the file as it is on disk at write time
// rather than on the scan-time snapshot. A concurrent agent result that clears the
// assignee in between makes the transform a no-op, which is why the transform is
// idempotent rather than guarded here.
func (v *vaultScanner) escalateCappedTask(ctx context.Context, relPath string) (bool, error) {
	fullAbsPath := filepath.Join(v.gitClient.Path(), relPath)
	wrote := false
	err := v.gitClient.AtomicReadModifyWriteAndCommitPush(
		ctx,
		fullAbsPath,
		func(current []byte) ([]byte, error) {
			next, changed, terr := escalateCappedContent(ctx, current, libtime.DateTime(time.Now()))
			if terr != nil {
				return nil, terr
			}
			if !changed {
				return current, nil
			}
			wrote = true
			return next, nil
		},
		fmt.Sprintf("[agent-task-controller] escalate capped task %s", relPath),
	)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "atomic escalation")
	}
	return wrote, nil
}

// escalateCappedContent applies the escalation to one file's bytes.
//
// Every clause re-reads the frontmatter rather than trusting the index, because this
// runs inside the atomic write against the on-disk content. That is what makes a
// retry safe: the second attempt sees the first attempt's cleared assignee and
// returns unchanged.
//
// A non-empty current_job is a hard skip. The executor refuses to spawn a capped task,
// but a job started before the cap was reached may still be running, and clearing that
// task's assignee mid-run would strand the result it is about to publish.
func escalateCappedContent(
	ctx context.Context,
	current []byte,
	now libtime.DateTime,
) ([]byte, bool, error) {
	frontmatterStr, err := extractFrontmatter(ctx, current)
	if err != nil {
		return nil, false, errors.Wrapf(ctx, err, "extract frontmatter")
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(frontmatterStr), &fmMap); err != nil {
		return nil, false, errors.Wrapf(ctx, err, "unmarshal frontmatter")
	}
	fm := lib.TaskFrontmatter(fmMap)
	if !isCappedIdle(fm) {
		return nil, false, nil
	}
	if fm.Status() != cappedIdleStatus {
		return nil, false, nil
	}
	if fm.CurrentJob() != "" {
		return nil, false, nil
	}
	agentName := result.ClearAssignee(fm)
	if agentName == "" {
		return nil, false, nil
	}
	// See cappedEscalatedStatus: without this the executor restores the assignee and
	// the scanner re-arms the budget, so the rescue would not survive one tick.
	fm["status"] = cappedEscalatedStatus
	body := extractBody(current)
	if !result.ContainsTriggerCapEscalation(body) {
		body += result.TriggerCapEscalationSection(
			fm.TriggerCount(),
			fm.MaxTriggers(),
			agentName,
			now,
		)
	}
	newFmYAML, err := yaml.Marshal(map[string]interface{}(fm))
	if err != nil {
		return nil, false, errors.Wrapf(ctx, err, "marshal frontmatter")
	}
	return []byte("---\n" + string(newFmYAML) + "---\n" + body), true, nil
}
