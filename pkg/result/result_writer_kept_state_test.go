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
		fakeGit       *mocks.GitClient
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

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			matches, err := filepath.Glob(filepath.Join(tmpDir, glob))
			if err != nil {
				return nil, err
			}
			var rel []string
			for _, m := range matches {
				r, _ := filepath.Rel(tmpDir, m)
				rel = append(rel, r)
			}
			return rel, nil
		}
		fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			return os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test-only path
		}
		fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
			_ context.Context,
			absPath string,
			modify func([]byte) ([]byte, error),
			_ string,
		) error {
			current, err := os.ReadFile(absPath) // #nosec G304 -- test helper
			if err != nil {
				return err
			}
			updated, err := modify(current)
			if err != nil {
				return err
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}

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
