// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
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
