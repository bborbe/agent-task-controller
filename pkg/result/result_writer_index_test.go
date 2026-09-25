// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"errors"
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

var _ = Describe("ResultWriter identifier index", func() {
	const (
		indexedID = "11111111-1111-4111-8111-111111111111"
		missingID = "22222222-2222-4222-8222-222222222222"
	)

	var (
		ctx      context.Context
		tmpDir   string
		taskDir  string
		fakeGit  *mocks.GitClient
		resolver *mocks.TaskPathResolver
		fakeTime *libtimemocks.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-index-test-*")
		Expect(err).NotTo(HaveOccurred())

		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

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
			ctx context.Context,
			absPath string,
			modify func([]byte) ([]byte, error),
			message string,
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

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))

		// Default miss: every spec that does not opt into a hit behaves exactly as
		// the walk-only path does today.
		resolver = &mocks.TaskPathResolver{}
		resolver.ResolveReturns("", false, nil)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	// newWriter constructs a writer bound to the same fakeGit the call-count
	// assertions read, so a count is never taken from a second, unused fake.
	newWriter := func(r result.TaskPathResolver) result.ResultWriter {
		return result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			r,
		)
	}

	writeTaskFile := func(name, content string) string {
		absPath := filepath.Join(tmpDir, taskDir, name)
		Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())
		return absPath
	}

	readFile := func(absPath string) string {
		content, err := os.ReadFile(absPath) // #nosec G304 -- test helper
		Expect(err).NotTo(HaveOccurred())
		return string(content)
	}

	payloadFor := func(id string) lib.Task {
		return lib.Task{
			TaskIdentifier: lib.TaskIdentifier(id),
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": id,
				"status":          "done",
				"phase":           "done",
			},
			Content: lib.TaskContent("New content\n"),
		}
	}

	It("costs one read and no listing for an indexed identifier", func() {
		taskFile := writeTaskFile(
			"indexed.md",
			"---\ntask_identifier: "+indexedID+
				"\nstatus: in-progress\nassignee: backtest-agent\n---\nOld content\n",
		)
		resolver.ResolveReturns("tasks/indexed.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
		Expect(fakeGit.ReadFileCallCount()).To(Equal(1))
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

	It("still resolves through the walk on an index miss", func() {
		taskFile := writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		written := readFile(taskFile)
		Expect(written).To(ContainSubstring("status: done"))
		Expect(written).To(ContainSubstring("phase: done"))
	})

	It("returns a resolver error without walking or writing", func() {
		resolver.ResolveReturns(
			"",
			false,
			errors.New(
				"duplicate task_identifier "+missingID+
					" in tasks/first.md and tasks/second.md",
			),
		)

		writer := newWriter(resolver)
		err := writer.WriteResult(ctx, payloadFor(missingID))

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("tasks/first.md"))
		Expect(err.Error()).To(ContainSubstring("tasks/second.md"))
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
		Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
	})

	It("selects the walk-only behaviour for a nil resolver", func() {
		taskFile := writeTaskFile(
			"walk-only.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)

		writer := newWriter(nil)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

	It("falls back to the walk when the indexed path cannot be read", func() {
		taskFile := writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)
		// A path that does not exist stands in for a hit whose file was deleted or
		// renamed since the last scan cycle.
		resolver.ResolveReturns("tasks/gone.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

	It("falls back to the walk when the indexed file has no frontmatter", func() {
		taskFile := writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)
		writeTaskFile("broken.md", "no frontmatter here\n")
		resolver.ResolveReturns("tasks/broken.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

	It("falls back to the walk when the indexed file's frontmatter will not unmarshal", func() {
		// ExtractFrontmatter succeeds on a scalar document, but unmarshalling it into a
		// frontmatter map fails, so the identifier cannot be verified and the hit is
		// refused — the walk would have skipped the same file for the same reason, so
		// fail-closed keeps the two mechanisms in agreement.
		taskFile := writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)
		writeTaskFile("scalar.md", "---\nscalar\n---\nbody\n")
		resolver.ResolveReturns("tasks/scalar.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

	It("falls back to the walk when the indexed path no longer carries the identifier", func() {
		// The index is rebuilt once per scan cycle, so a file repurposed since the last
		// cycle still maps indexedID -> its path. Writing there would put this task's
		// result onto a file that now belongs to a different task — the 2026-08-31
		// incident — and MergeFrontmatter would then overwrite the victim's
		// task_identifier, making the hijack permanent. The walk compares the identifier
		// before accepting a match, so the hit path must too.
		other := writeTaskFile(
			"repurposed.md",
			"---\ntask_identifier: "+missingID+"\nstatus: in-progress\n---\nOther task\n",
		)
		taskFile := writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)
		resolver.ResolveReturns("tasks/repurposed.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
		Expect(readFile(other)).NotTo(ContainSubstring("status: done"))
		Expect(readFile(other)).To(ContainSubstring("task_identifier: " + missingID))
	})
})
