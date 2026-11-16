// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

// healthcheckTaskType is the task_type of the records this gauge watches. The probe
// emitter stamps every probe record with it, so a `llm` or `pr-review` record that
// happens to sit at a non-terminal phase is not a probe and must not inflate the
// count.
const healthcheckTaskType = "healthcheck"

// parkedProbeNamePrefix pre-filters the vault listing to the probe files before any
// of them is read. The scan cycle already lists the task directory and the vault
// holds thousands of task files, so the prefix check costs one string comparison per
// path where reading every file to find the probe rows would cost a read per file.
// The prefix is the emitter's own filename shape (`probe-<agent>-<stage>.md`); every
// candidate is then confirmed against its frontmatter, so a file that merely looks
// like a probe cannot be counted.
const parkedProbeNamePrefix = "probe-"

// parkedProbeTerminalPhases and parkedProbeTerminalStatuses are the values that end a
// record's life. A probe counts as parked only when it carries neither, so a record
// that reached a terminal phase or status is never counted as stuck.
var (
	parkedProbeTerminalPhases   = map[string]bool{"done": true}
	parkedProbeTerminalStatuses = map[string]bool{"completed": true, "aborted": true}
)

// countParkedProbes returns how many healthcheck probe records are parked: their
// task_type is healthcheck, neither their phase nor their status is terminal, and no
// job is running them.
//
// It is the sibling of countStuckPlanningTasks, not a widening of it, and the two
// watch disjoint populations. That gauge measures a run that started and stalled —
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
// It reads only the files whose name matches parkedProbeNamePrefix and never mutates
// anything — the scan cycle owns all writes. A file that cannot be read or parsed is
// skipped silently: this is a gauge, and one unreadable file must not blank it.
func (v *vaultScanner) countParkedProbes(ctx context.Context, paths []string) int {
	parked := 0
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return parked
		default:
		}
		if !strings.HasPrefix(filepath.Base(relPath), parkedProbeNamePrefix) {
			continue
		}
		if v.isParkedProbe(ctx, relPath) {
			parked++
		}
	}
	return parked
}

// isParkedProbe reports whether one probe file meets every clause of the parked
// definition. Each clause is checked against the parsed frontmatter rather than the
// filename, so a file that matches the name prefix but not the contract is not
// counted.
func (v *vaultScanner) isParkedProbe(ctx context.Context, relPath string) bool {
	content, err := v.ops.readFile(ctx, relPath)
	if err != nil {
		glog.V(4).Infof("parked-probe gauge: read %s: %v", relPath, err)
		return false
	}
	fmMap, ok := parseFrontmatterMap(ctx, content)
	if !ok {
		return false
	}
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
