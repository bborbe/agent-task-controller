// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("countStuckPlanningTasks", func() {
	const (
		path      = "Update Go bborbe-alert a34dc9e.md"
		otherPath = "Update Go bborbe-vault-cli 1111111.md"
	)

	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	oldStart := now.Add(-25 * time.Hour).Format(time.RFC3339)
	recentStart := now.Add(-1 * time.Hour).Format(time.RFC3339)

	failureBody := "\n# Update Go\n\n## Failure\n\n- **Reason:**\n\n```\ndeadline_exceeded\n```\n"
	cleanBody := "\n# Update Go\n\nNothing wrong here.\n"

	taskFile := func(taskType, phase, status, jobStartedAt, body string) string {
		return "---\n" +
			"task_type: " + taskType + "\n" +
			"phase: " + phase + "\n" +
			"status: " + status + "\n" +
			"job_started_at: \"" + jobStartedAt + "\"\n" +
			"---\n" + body
	}

	newScanner := func(files map[string]string) *vaultScanner {
		return &vaultScanner{
			ops: fileOps{
				readFile: func(_ context.Context, relPath string) ([]byte, error) {
					content, ok := files[relPath]
					if !ok {
						return nil, os.ErrNotExist
					}
					return []byte(content), nil
				},
			},
		}
	}

	It("counts a planning task whose job started more than 24h ago and failed", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "planning", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(1))
	})

	It("does not count a job that started less than 24h ago", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "planning", "in_progress", recentStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(0))
	})

	It("does not count a task that is no longer in planning", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "human_review", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(0))
	})

	It("does not count a task with no ## Failure entry", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "planning", "in_progress", oldStart, cleanBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(0))
	})

	It("does not count a decision task, which never runs a job", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go-decision", "planning", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(0))
	})

	It("does not count a task with no parseable job_started_at", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "planning", "in_progress", "", failureBody),
		})
		Expect(v.countStuckPlanningTasks(context.Background(), []string{path}, now)).To(Equal(0))
	})

	It("skips files whose name does not carry the update-go prefix", func() {
		v := newScanner(map[string]string{
			path:                    taskFile("github-update-go", "planning", "in_progress", oldStart, failureBody),
			"Release bborbe-alert x.md": taskFile("github-release", "planning", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(
			context.Background(),
			[]string{path, "Release bborbe-alert x.md"},
			now,
		)).To(Equal(1))
	})

	It("counts every stuck task across the vault", func() {
		v := newScanner(map[string]string{
			path:      taskFile("github-update-go", "planning", "in_progress", oldStart, failureBody),
			otherPath: taskFile("github-update-go", "planning", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(
			context.Background(),
			[]string{path, otherPath},
			now,
		)).To(Equal(2))
	})

	It("skips an unreadable file instead of failing the count", func() {
		v := newScanner(map[string]string{
			path: taskFile("github-update-go", "planning", "in_progress", oldStart, failureBody),
		})
		Expect(v.countStuckPlanningTasks(
			context.Background(),
			[]string{path, otherPath},
			now,
		)).To(Equal(1))
	})
})
