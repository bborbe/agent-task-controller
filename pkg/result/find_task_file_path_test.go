// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	stdtime "time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

var _ = Describe("FindTaskFilePath", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("calls gitClient.ListFiles + ReadFile with the expected glob and matched paths", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns([]string{"tasks/a.md", "tasks/b.md"}, nil)
		fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: foo\n---\n"), nil)
		fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: bar\n---\n"), nil)

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "bar")
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/b.md"))
		Expect(fakeGC.ListFilesCallCount()).To(Equal(1))
		_, glob := fakeGC.ListFilesArgsForCall(0)
		Expect(glob).To(Equal("tasks/*.md"))
		Expect(fakeGC.ReadFileCallCount()).To(BeNumerically(">=", 1))
	})

	// Regression: 2026-08-31. Two Schedule CRs sharing a name across the dev and
	// prod fleets minted the same UUID5, so two task files carried one
	// task_identifier. The match loop had no break and no duplicate check, so it
	// silently kept the LAST path from an unsorted ListFiles — a result was written
	// onto `Sentry Alert Fan-Out - 2026-08-31` and marked it done/completed for a
	// run no executor performed. Picking either file is wrong; the only safe
	// outcome is a loud error.
	It("returns an error when two files share one task_identifier", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns([]string{"tasks/first.md", "tasks/second.md"}, nil)
		fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: dup\n---\n"), nil)
		fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: dup\n---\n"), nil)

		matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "dup")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier"))
		Expect(err.Error()).To(ContainSubstring("tasks/first.md"))
		Expect(err.Error()).To(ContainSubstring("tasks/second.md"))
		Expect(matchedRelPath).To(Equal(""))
		Expect(fm).To(BeNil())
	})

	It("still resolves when a duplicate exists for a DIFFERENT identifier", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns(
			[]string{"tasks/a.md", "tasks/b.md", "tasks/c.md"},
			nil,
		)
		fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: dup\n---\n"), nil)
		fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: dup\n---\n"), nil)
		fakeGC.ReadFileReturnsOnCall(2, []byte("---\ntask_identifier: target\n---\n"), nil)

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "target")
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/c.md"))
	})

	It("returns empty path when no file matches", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns([]string{"tasks/a.md"}, nil)
		fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: other\n---\n"), nil)

		matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "missing")
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal(""))
		Expect(fm).To(BeNil())
	})

	It("skips files that fail to read", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns([]string{"tasks/bad.md", "tasks/good.md"}, nil)
		fakeGC.ReadFileReturnsOnCall(0, nil, errTest)
		fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: target\n---\n"), nil)

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "target")
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/good.md"))
	})

	It("skips files with invalid frontmatter", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns([]string{"tasks/bad.md", "tasks/good.md"}, nil)
		fakeGC.ReadFileReturnsOnCall(0, []byte("no frontmatter here"), nil)
		fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: target\n---\n"), nil)

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "target")
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/good.md"))
	})

	It("returns error when ListFiles fails", func() {
		fakeGC := &mocks.GitClient{}
		fakeGC.ListFilesReturns(nil, errTest)

		_, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "any")
		Expect(err).To(HaveOccurred())
	})
})

// Bounded retry on a not-found task file. The controller lists files over git-rest
// HTTP rather than a local clone, so a result can arrive before the file it belongs
// to is visible; without a retry that result is dropped for good. Retrying in-process
// (rather than returning an error) is deliberate — the consumer runs
// SkipCorruptBatches:false, so erroring on a permanently-missing file would block the
// partition.
var _ = Describe("WriteResult not-found retry", func() {
	var (
		ctx      context.Context
		fakeGit  *mocks.GitClient
		fakeTime *libtimemocks.CurrentDateTimeGetter
		fakeWait *libtimemocks.WaiterDuration
		writer   result.ResultWriter
		taskDir  string
		task     lib.Task
	)

	BeforeEach(func() {
		ctx = context.Background()
		taskDir = "tasks"
		fakeGit = &mocks.GitClient{}
		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(stdtime.Date(2026, 8, 31, 12, 0, 0, 0, stdtime.UTC)))
		fakeWait = &libtimemocks.WaiterDuration{}
		writer = result.NewResultWriter(fakeGit, taskDir, fakeTime, metrics.New(), fakeWait)
		task = lib.Task{TaskIdentifier: "late-arrival"}
	})

	It("retries and resolves when the file appears on a later attempt", func() {
		// First sweep sees nothing (git-rest has not pulled yet), second sees the file.
		fakeGit.ListFilesReturnsOnCall(0, []string{}, nil)
		fakeGit.ListFilesReturnsOnCall(1, []string{"tasks/late.md"}, nil)
		fakeGit.ReadFileReturns(
			[]byte("---\ntask_identifier: late-arrival\nstatus: in_progress\n---\nbody\n"),
			nil,
		)

		err := writer.WriteResult(ctx, task)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeGit.ListFilesCallCount()).To(Equal(2))
		Expect(fakeWait.WaitCallCount()).To(Equal(1))
	})

	It("gives up after the attempt budget without returning an error", func() {
		// A permanently missing file must NOT error — that would block the partition.
		fakeGit.ListFilesReturns([]string{}, nil)

		err := writer.WriteResult(ctx, task)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeGit.ListFilesCallCount()).To(Equal(3))
		Expect(fakeWait.WaitCallCount()).To(Equal(2))
	})

	It("propagates a cancelled context from the wait instead of spinning", func() {
		fakeGit.ListFilesReturns([]string{}, nil)
		fakeWait.WaitReturns(context.Canceled)

		err := writer.WriteResult(ctx, task)
		Expect(err).To(HaveOccurred())
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
	})
})
