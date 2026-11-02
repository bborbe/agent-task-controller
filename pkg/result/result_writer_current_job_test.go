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

var _ = Describe("ClearCurrentJobIfTerminal", func() {
	It("empties current_job for completed", func() {
		fm := lib.TaskFrontmatter{"status": "completed", "current_job": "some-job"}
		result.ClearCurrentJobIfTerminal(fm)
		Expect(fm["current_job"]).To(Equal(""))
	})

	It("empties current_job for aborted", func() {
		fm := lib.TaskFrontmatter{"status": "aborted", "current_job": "some-job"}
		result.ClearCurrentJobIfTerminal(fm)
		Expect(fm["current_job"]).To(Equal(""))
	})

	It("leaves current_job unchanged for a non-terminal status", func() {
		fm := lib.TaskFrontmatter{"status": "in_progress", "current_job": "some-job"}
		result.ClearCurrentJobIfTerminal(fm)
		Expect(fm["current_job"]).To(Equal("some-job"))
	})

	It("does not introduce current_job when the key is absent", func() {
		fm := lib.TaskFrontmatter{"status": "completed"}
		result.ClearCurrentJobIfTerminal(fm)
		_, present := fm["current_job"]
		Expect(present).To(BeFalse())
	})

	It("keeps current_job empty when it is already empty", func() {
		fm := lib.TaskFrontmatter{"status": "completed", "current_job": ""}
		result.ClearCurrentJobIfTerminal(fm)
		Expect(fm["current_job"]).To(Equal(""))
	})
})

var _ = Describe("ResultWriter current_job clearing", func() {
	var (
		ctx        context.Context
		tmpDir     string
		taskDir    string
		fakeGit    *mocks.GitClient
		fakeTime   *libtimemocks.CurrentDateTimeGetter
		writer     result.ResultWriter
		taskFile   lib.Task
		identifier lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-current-job-test-*")
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
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)))

		identifier = lib.TaskIdentifier("test-task-uuid-1234")
		writer = result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	writeTaskFile := func(name, content string) string {
		absPath := filepath.Join(tmpDir, taskDir, name)
		Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())
		return absPath
	}

	It("empties current_job when the incoming status is completed", func() {
		writeTaskFile(
			"my-task.md",
			"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\nassignee: claude\ncurrent_job: some-job\n---\nbody\n",
		)
		taskFile = lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": "test-task-uuid-1234",
				"status":          "completed",
			},
			Content: lib.TaskContent("done\n"),
		}
		Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
		written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
		Expect(string(written)).To(ContainSubstring("current_job: \"\""))
	})

	It("keeps current_job when the incoming status is in_progress", func() {
		writeTaskFile(
			"my-task.md",
			"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\nassignee: claude\ncurrent_job: some-job\n---\nbody\n",
		)
		taskFile = lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": "test-task-uuid-1234",
				"status":          "in_progress",
			},
			Content: lib.TaskContent("still running\n"),
		}
		Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
		written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
		Expect(string(written)).To(ContainSubstring("current_job: some-job"))
	})
})
