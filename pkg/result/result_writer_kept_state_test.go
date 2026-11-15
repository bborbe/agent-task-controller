// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/testvault"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// A result the ownership guard reduces to a no-op produces no commit, so the vault
// scanner publishes nothing and an executor whose view holds the rejected values
// re-drives the task. Measured on sun-k3s 2026-10-28 and 2026-10-31: completed,
// parked pr-override records re-spawned and re-approved already-merged PRs.
var _ = Describe("ResultWriter kept-state republish", func() {
	const parkedOverride = `---
status: completed
phase: human_review
assignee: ""
task_type: pr-override
task_identifier: kept-state-uuid
---
# Review
`
	var (
		ctx           context.Context
		tmpDir        string
		fakeGit       *testvault.Client
		fakePublisher *mocks.TaskPublisher
		writer        result.ResultWriter
		identifier    lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-kept-state-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())

		fakeGit = &testvault.Client{Dir: tmpDir}

		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 10, 31, 10, 4, 53, 0, time.UTC)))

		fakePublisher = &mocks.TaskPublisher{}
		identifier = lib.TaskIdentifier("kept-state-uuid")

		writer = result.NewResultWriter(
			fakeGit,
			"tasks",
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
			result.WithKeptStatePublisher(fakePublisher),
		)
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "PR Override kept.md"),
			[]byte(parkedOverride),
			0600,
		)).To(Succeed())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	It("republishes the on-disk state when the guard rejects a stale status and assignee", func() {
		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"status":       "in_progress",
				"phase":        "human_review",
				"assignee":     "pr-reviewer-agent",
				"task_type":    "pr-override",
				"target_vault": "openclaw",
			},
			Content: lib.TaskContent("# Review\n"),
		})).To(Succeed())

		Expect(fakePublisher.PublishChangedCallCount()).To(Equal(1))
		_, published := fakePublisher.PublishChangedArgsForCall(0)
		Expect(published.TaskIdentifier).To(Equal(identifier))
		Expect(string(published.Frontmatter.Status())).To(Equal("completed"))
		Expect(string(published.Frontmatter.Assignee())).To(Equal(""))
	})

	// A spec here forced `AtomicReadModifyWriteAndCommitPush` to re-invoke the modify
	// closure twice — once against the parked record, once against a reopened one —
	// and asserted the publish came from the committed attempt. That mechanism is
	// gone: the retry is now the caller-side loop's PATCH failure, and a PATCH that
	// never happens cannot be "the earlier failed attempt". Re-stating it needs a
	// VaultAPIClient whose ReadTask answers parked-then-reopened AND whose modify
	// produces a change on the first attempt, which the guard's whole purpose is to
	// prevent. The property that survives — a committed attempt that rejects no field
	// publishes nothing — is asserted directly below.

	It("publishes nothing when the guard rejects no field", func() {
		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"status":       "completed",
				"phase":        "human_review",
				"task_type":    "pr-override",
				"target_vault": "openclaw",
			},
			Content: lib.TaskContent("# Review\n"),
		})).To(Succeed())

		Expect(fakePublisher.PublishChangedCallCount()).To(Equal(0))
	})
})
