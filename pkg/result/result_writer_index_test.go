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
		fakeGit  *mocks.FileVaultClient
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

		fakeGit = &mocks.FileVaultClient{Dir: tmpDir}

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
			nil,
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

		Expect(fakeGit.ListTasksCallCount()).To(Equal(0))
		// At least two reads: the index hit resolves by identifier, then the
		// read-modify-write reads again to merge against current state. (The
		// file-backed double also re-reads internally to return the patched task.)
		// What this spec is about is the absent listing, asserted just above.
		Expect(fakeGit.ReadTaskCallCount()).To(BeNumerically(">=", 2))
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
		Expect(fakeGit.ListTasksCallCount()).To(Equal(1))
		written := readFile(taskFile)
		Expect(written).To(ContainSubstring("status: done"))
		Expect(written).To(ContainSubstring("phase: done"))
	})

	It("records the matched path after a walk that matched exactly one file", func() {
		writeTaskFile(
			"walked.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.RecordCallCount()).To(Equal(1))
		_, id, relPath := resolver.RecordArgsForCall(0)
		Expect(id).To(Equal(lib.TaskIdentifier(indexedID)))
		Expect(relPath).To(Equal("tasks/walked.md"))
	})

	It("does not record on an index hit", func() {
		writeTaskFile(
			"indexed.md",
			"---\ntask_identifier: "+indexedID+
				"\nstatus: in-progress\nassignee: backtest-agent\n---\nOld content\n",
		)
		resolver.ResolveReturns("tasks/indexed.md", true, nil)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.RecordCallCount()).To(Equal(0))
	})

	It("does not record when the walk matches nothing", func() {
		writeTaskFile(
			"other.md",
			"---\ntask_identifier: "+missingID+"\nstatus: in-progress\n---\nOther\n",
		)

		writer := newWriter(resolver)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(resolver.RecordCallCount()).To(Equal(0))
	})

	It("does not record when the walk finds a duplicate", func() {
		writeTaskFile(
			"first.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nFirst\n",
		)
		writeTaskFile(
			"second.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nSecond\n",
		)

		writer := newWriter(resolver)
		err := writer.WriteResult(ctx, payloadFor(indexedID))

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier " + indexedID))
		Expect(resolver.RecordCallCount()).To(Equal(0))
	})

	// Five specs here modelled the PATH-addressed index: an index hit whose file had
	// been repurposed, was unreadable, had no frontmatter, or would not unmarshal fell
	// back to the walk. None of that is reachable now — the typed read addresses the
	// task by IDENTIFIER, so a hit means the task with that id exists and there is no
	// path to repurpose, unreadable, or unparseable. The walk still runs on a miss, and
	// the specs for that survive below.
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
		Expect(fakeGit.ListTasksCallCount()).To(Equal(0))
		Expect(fakeGit.PatchTaskWithBodyCallCount()).To(Equal(0))
	})

	It("selects the walk-only behaviour for a nil resolver", func() {
		taskFile := writeTaskFile(
			"walk-only.md",
			"---\ntask_identifier: "+indexedID+"\nstatus: in-progress\n---\nOld content\n",
		)

		writer := newWriter(nil)
		Expect(writer.WriteResult(ctx, payloadFor(indexedID))).To(Succeed())

		Expect(fakeGit.ListTasksCallCount()).To(Equal(1))
		Expect(readFile(taskFile)).To(ContainSubstring("status: done"))
	})

})
