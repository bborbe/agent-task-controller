// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"strings"
	"time"
)

// stuckPlanningTaskType is the task_type whose backlog this gauge watches. Only the
// plain update task counts: `github-update-go-decision` rows are operator questions,
// not agent runs, and would inflate the number without any job ever starting.
const stuckPlanningTaskType = "github-update-go"

// stuckPlanningMaxAge is how long a task may sit in planning after its job started
// before it counts as stuck. Pinned by the SC6 acceptance criterion (> 24 h).
const stuckPlanningMaxAge = 24 * time.Hour

// isStuckPlanningTask reports whether one task record meets every clause of the stuck
// definition: a `github-update-go` task still at phase planning, its job started more
// than stuckPlanningMaxAge ago, and a `## Failure` entry in the body.
//
// It takes the frontmatter map and the content that recomputeVaultMetrics has already
// produced, so it costs no read and no listing of its own. Every clause is checked
// against the parsed frontmatter rather than the filename: a file that merely looks
// like an update task cannot be counted. The name-prefix pre-filter the scan-cycle
// version needed to avoid reading ~12k files is unnecessary here, because this pass
// reads every file anyway.
func isStuckPlanningTask(fmMap map[string]interface{}, content []byte, now time.Time) bool {
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
