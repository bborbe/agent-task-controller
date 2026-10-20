// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
	"gopkg.in/yaml.v3"
)

// stuckPlanningTaskType is the task_type whose backlog this gauge watches. Only the
// plain update task counts: `github-update-go-decision` rows are operator questions,
// not agent runs, and would inflate the number without any job ever starting.
const stuckPlanningTaskType = "github-update-go"

// stuckPlanningMaxAge is how long a task may sit in planning after its job started
// before it counts as stuck. Pinned by the SC6 acceptance criterion (> 24 h).
const stuckPlanningMaxAge = 24 * time.Hour

// stuckPlanningNamePrefix pre-filters the vault listing to the update-go task files
// before any of them is read. The vault holds ~12k task files and the scanner runs
// every few minutes, so reading all of them to find ~300 update rows would cost more
// than the metric is worth. The prefix is the emitter's own filename shape
// (`Update Go <owner>-<repo> <sha>.md`, see /github-update-go-repo-trigger § 6);
// every candidate is then confirmed against its frontmatter, so a file that merely
// looks like an update task cannot be counted.
const stuckPlanningNamePrefix = "Update Go "

// countStuckPlanningTasks returns how many github-update-go tasks are stuck: still at
// phase planning, with a job that started more than stuckPlanningMaxAge ago, and a
// `## Failure` entry in the body.
//
// It reads only the files whose name matches stuckPlanningNamePrefix and never mutates
// anything — the scan cycle owns all writes. A file that cannot be read or parsed is
// skipped silently: this is a gauge, and one unreadable file must not blank it.
func (v *vaultScanner) countStuckPlanningTasks(
	ctx context.Context,
	paths []string,
	now time.Time,
) int {
	stuck := 0
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return stuck
		default:
		}
		if !strings.HasPrefix(filepath.Base(relPath), stuckPlanningNamePrefix) {
			continue
		}
		if v.isStuckPlanningTask(ctx, relPath, now) {
			stuck++
		}
	}
	return stuck
}

// isStuckPlanningTask reports whether one task file meets every clause of the stuck
// definition. Each clause is checked against the parsed frontmatter rather than the
// filename, so a file that matches the name prefix but not the contract is not counted.
func (v *vaultScanner) isStuckPlanningTask(ctx context.Context, relPath string, now time.Time) bool {
	content, err := v.ops.readFile(ctx, relPath)
	if err != nil {
		glog.V(4).Infof("stuck-planning gauge: read %s: %v", relPath, err)
		return false
	}
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		return false
	}
	dedupedYAML, _, err := DeduplicateFrontmatter(ctx, fmYAML)
	if err != nil {
		return false
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(dedupedYAML), &fmMap); err != nil {
		return false
	}
	if stringField(fmMap, "task_type") != stuckPlanningTaskType {
		return false
	}
	if stringField(fmMap, "phase") != "planning" {
		return false
	}
	if stringField(fmMap, "status") != "in_progress" {
		return false
	}
	startedAt, err := time.Parse(time.RFC3339, stringField(fmMap, "job_started_at"))
	if err != nil {
		// No parseable job start means no run ever began, so the task is queued
		// rather than stuck. Counting it would make the alert fire on a backlog
		// the fleet has not yet picked up.
		return false
	}
	if now.Sub(startedAt) <= stuckPlanningMaxAge {
		return false
	}
	return strings.Contains(extractBody(content), "\n## Failure")
}

// stringField reads a frontmatter key as a string, returning "" for a missing or
// non-string value. Frontmatter is a generic map, so every value arrives as interface{}
// and a numeric or nested field must not panic the scan.
func stringField(fmMap map[string]interface{}, key string) string {
	value, ok := fmMap[key]
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return s
}
