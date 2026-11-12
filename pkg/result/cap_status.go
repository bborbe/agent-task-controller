// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import "strings"

// CappedEscalatedStatus is the status a cap escalation moves a task to.
//
// Clearing the assignee alone is NOT durable. A task with an empty assignee whose
// task_type still resolves to a Config is treated by the executor as parked and
// gets its routing value written straight back (restoreParkedAssignee in
// agent-task-executor), and the scanner then reads that empty → named transition as
// an operator re-delegation and zeroes trigger_count — which re-arms the budget and
// the whole cycle repeats. Measured on nuke prod 2026-11-12: probe-sentry-fix-agent
// went trigger_count 8 → 0 → 5 → 8 inside 90 seconds, with the assignee restored on
// every ~60 s reconcile tick.
//
// `hold` breaks that loop at the executor's own eligibility filter: the default
// trigger statuses are `[in_progress]` only, so a held task is refused before the
// restore branch is ever reached.
//
// This lives in `result` rather than `scanner` because both escalation paths need it
// — the increment executor and the result writer — and `scanner` already imports
// `result`. The capped-idle sweep in `pkg/scanner/capped_idle.go` aliases this
// constant, so there is one definition of the parked status, not three.
const CappedEscalatedStatus = "hold"

// triggerCapEscalationHeading is the body heading every escalation section opens
// with. Not spelled outside this file: callers use ContainsTriggerCapEscalation or
// CollapseTriggerCapEscalation.
const triggerCapEscalationHeading = "## Trigger Cap Escalation"

// CollapseTriggerCapEscalation returns body carrying at most one
// `## Trigger Cap Escalation` section — the first, kept verbatim, with every later
// duplicate removed. A body with no section is returned unchanged.
//
// Keeping the first rather than re-rendering one is deliberate. Re-rendering would
// re-stamp the timestamp on every result write, so an escalation that is already
// parked would churn its own body on each tick; the first section is the one that
// recorded the escalation when it actually happened.
//
// The duplicate guard alone is not sufficient. Three writers escalate — the
// increment executor, the result writer, and the capped-idle sweep — and all three
// guard with a read-then-write check, so a same-second race lets each observe "not
// present" and append. Measured 2026-11-09 on probe-sentry-fix-agent-prod: three
// sections, all stamped 2026-11-09T14:17:36Z. Collapsing at write time converges
// instead of preserving whichever writer lost the race.
func CollapseTriggerCapEscalation(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	seen := false
	skipping := false
	for _, line := range lines {
		if line == triggerCapEscalationHeading {
			if seen {
				skipping = true
				continue
			}
			seen = true
			skipping = false
			kept = append(kept, line)
			continue
		}
		if skipping {
			if strings.HasPrefix(line, "## ") {
				skipping = false
			} else {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
