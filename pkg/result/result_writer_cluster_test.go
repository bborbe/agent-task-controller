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

// The operator-pinned cluster guard's write-path coverage lives in its own file so
// the main writer suite stays under the linter's file-length limit.
var _ = Describe("ResultWriter cluster pin", func() {
	var (
		ctx        context.Context
		tmpDir     string
		taskDir    string
		fakeGit    *mocks.GitClient
		fakeTime   *libtimemocks.CurrentDateTimeGetter
		writer     result.ResultWriter
		identifier lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-cluster-test-*")
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

	DescribeTable(
		"the on-disk cluster pin always wins and an incoming cluster never introduces the key",
		func(onDiskFM string, incomingFM lib.TaskFrontmatter, present, absent []string) {
			writeTaskFile("my-task.md", "---\n"+onDiskFM+"---\n## Result\nStatus: failed\n")
			taskFile := lib.Task{
				TaskIdentifier: identifier,
				Frontmatter:    incomingFM,
				Content:        lib.TaskContent("## Result\nStatus: failed\n"),
			}
			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
			written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			s := string(written)
			for _, want := range present {
				Expect(s).To(ContainSubstring(want))
			}
			for _, unwanted := range absent {
				Expect(s).NotTo(ContainSubstring(unwanted))
			}
		},
		Entry(
			"operator removed cluster while the job ran: the result write does not restore it",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\n",
			lib.TaskFrontmatter{
				"task_identifier": "test-task-uuid-1234",
				"status":          "in_progress",
				"phase":           "ai_review",
				"cluster":         "sun",
			},
			[]string{"phase: ai_review"},
			[]string{"cluster:"},
		),
		Entry(
			"keeps the on-disk cluster over a stale incoming value",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\ncluster: nuke\n",
			lib.TaskFrontmatter{
				"task_identifier": "test-task-uuid-1234",
				"status":          "in_progress",
				"phase":           "ai_review",
				"cluster":         "sun",
			},
			[]string{"cluster: nuke"},
			[]string{"cluster: sun"},
		),
	)
})
