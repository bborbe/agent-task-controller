// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

const (
	// taskAgeOneDay is the exclusive upper bound of the lt_1d bucket.
	taskAgeOneDay = 24 * time.Hour
	// taskAgeSevenDays is the inclusive upper bound of the 1d_7d bucket
	// (604800 seconds), matching the alert expression `> 604800`.
	taskAgeSevenDays = 7 * 24 * time.Hour
)

// taskAgeTimestampFields is the age precedence, first parseable value wins.
var taskAgeTimestampFields = []string{"created", "created_date", "queued_at", "job_started_at"}

// taskAgeState is the outcome of classifying one file for the age summary.
type taskAgeState int

const (
	// taskAgeTerminal is a completed or aborted record: skipped, counted in no bucket.
	taskAgeTerminal taskAgeState = iota
	// taskAgeUndated is a non-terminal record with none of the four fields parseable:
	// counted in the undated bucket.
	taskAgeUndated
	// taskAgeDated is a non-terminal record with a parseable timestamp: age is its
	// truncated, clamped age and is classified into a bucket.
	taskAgeDated
	// taskAgeSkipped is a file that cannot be read, carries no frontmatter, or carries
	// frontmatter that will not deduplicate or unmarshal: skipped, counted in no bucket.
	taskAgeSkipped
)

// recomputeTaskAgeMetrics lists the task directory once and publishes the
// task-age series from a single clock read. It never pulls, scans, writes,
// commits or sends on the results channel.
func (v *vaultScanner) recomputeTaskAgeMetrics(ctx context.Context) {
	now := time.Now()
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("task age metrics: list %s failed: %v", glob, err)
		return
	}
	v.publishTaskAgeMetrics(ctx, paths, now, v.vaultName)
}

// publishTaskAgeMetrics reads the given task files and publishes the task-age
// gauges. It is read-only and never writes, pulls or scans.
func (v *vaultScanner) publishTaskAgeMetrics(
	ctx context.Context,
	paths []string,
	now time.Time,
	vaultName string,
) {
	counts := make(map[metrics.TaskAgeBucket]int, len(metrics.AvailableTaskAgeBuckets))
	oldest := time.Duration(0)
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return
		default:
		}
		age, state := v.taskAgeFromFile(ctx, relPath, now)
		switch state {
		case taskAgeTerminal, taskAgeSkipped:
			continue
		case taskAgeUndated:
			counts[metrics.TaskAgeBucketUndated]++
		case taskAgeDated:
			counts[taskAgeBucketOf(age)]++
			if age > oldest {
				oldest = age
			}
		}
	}
	v.metrics.TaskOldestAgeSeconds(vaultName).Set(float64(oldest / time.Second))
	for _, bucket := range metrics.AvailableTaskAgeBuckets {
		v.metrics.TaskAgeBucketTasks(vaultName, bucket).Set(float64(counts[bucket]))
	}
}

// taskAgeFromFile classifies one task file. For taskAgeDated, age is truncated to
// whole seconds and clamped at 0; it is 0 for every other state.
func (v *vaultScanner) taskAgeFromFile(
	ctx context.Context,
	relPath string,
	now time.Time,
) (age time.Duration, state taskAgeState) {
	content, err := v.ops.readFile(ctx, relPath)
	if err != nil {
		return 0, taskAgeSkipped
	}
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		return 0, taskAgeSkipped
	}
	deduped, _, err := DeduplicateFrontmatter(ctx, fmYAML)
	if err != nil {
		return 0, taskAgeSkipped
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(deduped), &fmMap); err != nil {
		return 0, taskAgeSkipped
	}
	status := stringField(fmMap, "status")
	if status == "completed" || status == "aborted" {
		return 0, taskAgeTerminal
	}
	for _, field := range taskAgeTimestampFields {
		parsed, ok := taskAgeTimestampOf(fmMap, field)
		if !ok {
			continue
		}
		age := now.Sub(parsed).Truncate(time.Second)
		if age < 0 {
			age = 0
		}
		return age, taskAgeDated
	}
	return 0, taskAgeUndated
}

// taskAgeBucketOf classifies a truncated age into exactly one bucket. The one-day
// boundary is exclusive, the seven-day boundary is inclusive.
func taskAgeBucketOf(age time.Duration) metrics.TaskAgeBucket {
	switch {
	case age < taskAgeOneDay:
		return metrics.TaskAgeBucketLt1d
	case age <= taskAgeSevenDays:
		return metrics.TaskAgeBucket1d7d
	default:
		return metrics.TaskAgeBucketGt7d
	}
}

// taskAgeTimestampOf reads one frontmatter timestamp field. A quoted value arrives as
// a string and is parsed as RFC3339 or YYYY-MM-DD; an unquoted value arrives from
// yaml.v3 as a time.Time and is used directly (a date-only value already carries
// midnight UTC). A missing or non-timestamp value is unparseable.
func taskAgeTimestampOf(fmMap map[string]interface{}, key string) (time.Time, bool) {
	switch value := fmMap[key].(type) {
	case string:
		return parseTaskAgeTimestamp(value)
	case time.Time:
		return value, true
	default:
		return time.Time{}, false
	}
}

// parseTaskAgeTimestamp parses one frontmatter timestamp value. RFC3339 (with or
// without fractional seconds) and date-only YYYY-MM-DD (read as midnight UTC) are
// accepted; anything else, including the empty string, is unparseable.
func parseTaskAgeTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}
