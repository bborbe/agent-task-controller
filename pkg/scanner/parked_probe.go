// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// healthcheckTaskType is the task_type of the records the parked-probe gauge
// watches. The probe emitter stamps every probe record with it, so a `llm` or
// `pr-review` record that happens to sit at a non-terminal phase is not a probe and
// must not inflate the count.
const healthcheckTaskType = "healthcheck"

// parkedProbeTerminalPhases and parkedProbeTerminalStatuses are the values that end a
// record's life. A probe counts as parked only when it carries neither, so a record
// that reached a terminal phase or status is never counted as stuck.
var (
	parkedProbeTerminalPhases   = map[string]bool{"done": true}
	parkedProbeTerminalStatuses = map[string]bool{"completed": true, "aborted": true}
)

// isParkedProbe reports whether one parsed task record is a parked healthcheck probe:
// its task_type is healthcheck, neither its phase nor its status is terminal, and no
// job is running it.
//
// It is the sibling of isStuckPlanningTask, not a widening of it, and the two watch
// disjoint populations. That one measures a run that started and stalled —
// github-update-go at phase planning with a job older than 24 h and a `## Failure`
// body. This one measures the opposite situation: a probe record that can never be
// re-triggered once it parks, so age and failure markers carry no signal and are
// deliberately not read. `trigger_count` is unused for the same reason — every row
// this gauge measures is permanently non-advancing, so a "not advancing" clause would
// be vacuously true for all of them and add nothing.
//
// The empty-`current_job` clause is what separates parked from running. A probe whose
// job is live is non-terminal in both phase and status, so without that clause every
// running probe would be counted as stuck.
//
// It takes the already-parsed frontmatter rather than a path. That is what lets the
// metrics pass classify a file from the map it has already built, so the count costs
// no second read and no listing of its own — and the metrics pass is the writer,
// because the scan cycle is dead on a trigger-only controller.
func isParkedProbe(fmMap map[string]interface{}) bool {
	if stringField(fmMap, "task_type") != healthcheckTaskType {
		return false
	}
	phase := stringField(fmMap, "phase")
	if phase == "" || parkedProbeTerminalPhases[phase] {
		return false
	}
	status := stringField(fmMap, "status")
	if status == "" || parkedProbeTerminalStatuses[status] {
		return false
	}
	return stringField(fmMap, "current_job") == ""
}
