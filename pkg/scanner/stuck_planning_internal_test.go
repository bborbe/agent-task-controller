// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("isStuckPlanningTask", func() {
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	oldStart := now.Add(-25 * time.Hour).Format(time.RFC3339)
	recentStart := now.Add(-1 * time.Hour).Format(time.RFC3339)

	failureBody := "\n# Update Go\n\n## Failure\n\n- **Reason:**\n\n```\ndeadline_exceeded\n```\n"
	cleanBody := "\n# Update Go\n\nNothing wrong here.\n"

	taskFile := func(taskType, phase, status, jobStartedAt, body string) []byte {
		return []byte("---\n" +
			"task_type: " + taskType + "\n" +
			"phase: " + phase + "\n" +
			"status: " + status + "\n" +
			"job_started_at: \"" + jobStartedAt + "\"\n" +
			"---\n" + body)
	}

	// classified parses the record exactly as recomputeVaultMetrics does, then asks
	// the predicate — so the spec exercises the real parse path, not a hand-built map.
	classified := func(raw []byte) bool {
		fmMap, ok := parseFrontmatterMap(context.Background(), raw)
		Expect(ok).To(BeTrue())
		return isStuckPlanningTask(fmMap, raw, now)
	}

	It("counts a planning task whose job started more than 24h ago and failed", func() {
		Expect(classified(taskFile(
			"github-update-go", "planning", "in_progress", oldStart, failureBody,
		))).To(BeTrue())
	})

	It("does not count a job that started less than 24h ago", func() {
		Expect(classified(taskFile(
			"github-update-go", "planning", "in_progress", recentStart, failureBody,
		))).To(BeFalse())
	})

	It("does not count a task that is no longer in planning", func() {
		Expect(classified(taskFile(
			"github-update-go", "human_review", "in_progress", oldStart, failureBody,
		))).To(BeFalse())
	})

	It("does not count a task that is not in progress", func() {
		Expect(classified(taskFile(
			"github-update-go", "planning", "hold", oldStart, failureBody,
		))).To(BeFalse())
	})

	It("does not count a task with no ## Failure entry", func() {
		Expect(classified(taskFile(
			"github-update-go", "planning", "in_progress", oldStart, cleanBody,
		))).To(BeFalse())
	})

	It("does not count a decision task, which never runs a job", func() {
		Expect(classified(taskFile(
			"github-update-go-decision", "planning", "in_progress", oldStart, failureBody,
		))).To(BeFalse())
	})

	It("does not count a task with no parseable job_started_at", func() {
		Expect(classified(taskFile(
			"github-update-go", "planning", "in_progress", "", failureBody,
		))).To(BeFalse())
	})
})
