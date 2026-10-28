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
	notifcmd "github.com/bborbe/notification/command/notification"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// retry_count is retired, so the frontmatter merge is incoming-wins for it: the
// ownership guard no longer pins the field. That makes the cap's input source
// load-bearing — an agent result is built from the spawn-time TASK_CONTENT
// snapshot and echoes retry_count: 0, so a cap reading the merged value would
// suppress the escalation a legacy record had already earned. These specs pin
// the on-disk read.
var _ = Describe("resultWriter retry_count retirement", func() {
	var (
		ctx        context.Context
		tmpDir     string
		taskDir    string
		fakeGit    *mocks.GitClient
		fakeTime   *libtimemocks.CurrentDateTimeGetter
		identifier lib.TaskIdentifier
		writer     result.ResultWriter
	)

	writeTaskFile := func(name, content string) {
		Expect(
			os.WriteFile(filepath.Join(tmpDir, taskDir, name), []byte(content), 0600),
		).To(Succeed())
	}

	readTaskFile := func(name string) string {
		written, err := os.ReadFile(
			filepath.Join(tmpDir, taskDir, name),
		) // #nosec G304 -- test-only path
		Expect(err).To(BeNil())
		return string(written)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-retry-retirement-*")
		Expect(err).NotTo(HaveOccurred())

		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			matches, globErr := filepath.Glob(filepath.Join(tmpDir, glob))
			if globErr != nil {
				return nil, globErr
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
			current, readErr := os.ReadFile(absPath) // #nosec G304 -- test-only path
			if readErr != nil {
				return readErr
			}
			updated, modifyErr := modify(current)
			if modifyErr != nil {
				return modifyErr
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test-only path
		}

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)))

		identifier = lib.TaskIdentifier("test-task-uuid-1234")

		writer = result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			notifcmd.NotificationPublishCommandSenderFunc(
				func(_ context.Context, _ notifcmd.NotificationPublishCommand) error {
					return nil
				},
			),
			nil,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	It(
		"escalates on the ON-DISK retry_count even when the incoming payload echoes a lower one",
		func() {
			writeTaskFile(
				"my-task.md",
				"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n"+
					"retry_count: 3\nmax_retries: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
			)
			taskFile := lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier": "test-task-uuid-1234",
					"status":          "in_progress",
					"phase":           "ai_review",
					"retry_count":     0,
					"max_retries":     3,
				},
				Content: lib.TaskContent("## Result\nStatus: failed\n"),
			}

			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

			s := readTaskFile("my-task.md")
			// The retired field is applied from the payload — the guard is gone.
			Expect(s).To(ContainSubstring("retry_count: 0"))
			// The cap still fires, from the value the file carried.
			Expect(s).To(ContainSubstring("## Retry Escalation"))
			Expect(s).To(ContainSubstring("**Attempts:** 3"))
			Expect(s).To(ContainSubstring("previous_assignee: claude"))
		},
	)

	It(
		"applies an incoming retry_count when the record never carried the key",
		func() {
			// The other half of the guard removal: an incoming value may now
			// introduce the key. Nothing writes it any more, so this can only come
			// from a stale payload — the spec pins that it lands rather than being
			// silently dropped, which is the behaviour the ownership list used to
			// enforce.
			writeTaskFile(
				"my-task.md",
				"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\n## Result\nStatus: failed\n",
			)
			taskFile := lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier": "test-task-uuid-1234",
					"status":          "in_progress",
					"phase":           "ai_review",
					"retry_count":     2,
				},
				Content: lib.TaskContent("## Result\nStatus: failed\n"),
			}

			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

			s := readTaskFile("my-task.md")
			Expect(s).To(ContainSubstring("retry_count: 2"))
			// No max_retries on the record, so the cap stays opt-out and nothing escalates.
			Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
		},
	)
})
