// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("IsOpenUpdateGoTaskForRepo", func() {
	build := func(taskType, repo, status string) []byte {
		return []byte(
			"---\n" +
				"task_type: " + taskType + "\n" +
				"repo: " + repo + "\n" +
				"status: " + status + "\n" +
				"---\n\nbody\n",
		)
	}

	It("accepts a still-open update task for the same repo", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/alert", "in_progress"),
			"bborbe/alert",
		)).To(BeTrue())
	})

	It("rejects a task for a different repo", func() {
		// The abort decision must never cross repos: two update tasks for two
		// repos are independent work, not a duplicate.
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/vault-cli", "in_progress"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects an already-aborted task", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/alert", "aborted"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects a decision task, which is an operator question and not a run", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go-decision", "bborbe/alert", "in_progress"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects content with no frontmatter", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			[]byte("not a task file\n"),
			"bborbe/alert",
		)).To(BeFalse())
	})
})
