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
	notifcore "github.com/bborbe/notification"
	notifcmd "github.com/bborbe/notification/command/notification"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// The notification lib ships no fake for NotificationPublishCommandSender (its
// mocks/ carries only the discord send, telegram send and store-tx fakes), so the
// specs use the library's own NotificationPublishCommandSenderFunc seam. It records
// exactly what these assertions need, with no generated mock to keep in sync.
var _ = Describe("resultWriter escalation notification", func() {
	var (
		ctx        context.Context
		tmpDir     string
		taskDir    string
		fakeGit    *mocks.GitClient
		fakeTime   *libtimemocks.CurrentDateTimeGetter
		identifier lib.TaskIdentifier
		published  []notifcmd.NotificationPublishCommand
		sendErr    error
		modifyRuns int
		writer     result.ResultWriter
	)

	writeTaskFile := func(name, content string) {
		Expect(
			os.WriteFile(filepath.Join(tmpDir, taskDir, name), []byte(content), 0600),
		).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-escalation-*")
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
		// modifyRuns > 1 simulates a commit retry: the git client re-invokes the
		// modify closure, which is exactly what must not produce a second publish.
		fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
			_ context.Context,
			absPath string,
			modify func([]byte) ([]byte, error),
			_ string,
		) error {
			current, readErr := os.ReadFile(absPath) // #nosec G304 -- test helper
			if readErr != nil {
				return readErr
			}
			var updated []byte
			for i := 0; i < modifyRuns; i++ {
				updated, err = modify(current)
				if err != nil {
					return err
				}
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)))

		identifier = lib.TaskIdentifier("test-task-uuid-1234")
		published = nil
		sendErr = nil
		modifyRuns = 1

		writer = result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			notifcmd.NotificationPublishCommandSenderFunc(
				func(_ context.Context, command notifcmd.NotificationPublishCommand) error {
					published = append(published, command)
					return sendErr
				},
			),
		)
	})

	// retryCapTask builds a task file already at its retry cap with the given assignee.
	retryCapTask := func(assignee string) lib.Task {
		writeTaskFile(
			"my-task.md",
			"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\n"+
				"retry_count: 3\nmax_retries: 3\nassignee: "+assignee+"\n---\n## Result\nStatus: failed\n",
		)
		return lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": "test-task-uuid-1234",
				"status":          "in_progress",
				"phase":           "execution",
				"retry_count":     3,
				"max_retries":     3,
				"assignee":        assignee,
			},
			Content: lib.TaskContent("## Result\nStatus: failed\n"),
		}
	}

	Context("a real escalation", func() {
		It(
			"publishes exactly one agent-escalation naming the task and the escalating agent",
			func() {
				Expect(writer.WriteResult(ctx, retryCapTask("claude"))).To(Succeed())

				Expect(published).To(HaveLen(1))
				Expect(published[0].Type).To(Equal(notifcore.AgentEscalationNotificationType))
				Expect(string(published[0].Message)).To(ContainSubstring("claude"))
				Expect(published[0].Metadata["previousAssignee"]).To(Equal("claude"))
				Expect(published[0].Metadata["taskIdentifier"]).To(Equal("test-task-uuid-1234"))
				Expect(published[0].Metadata["taskName"]).To(Equal("my-task"))
				Expect(
					string(published[0].Message),
				).To(ContainSubstring("obsidian://open?vault=openclaw"))
			},
		)

		It("sets no Target, leaving the channel decision to the deployed routing table", func() {
			Expect(writer.WriteResult(ctx, retryCapTask("claude"))).To(Succeed())

			Expect(published).To(HaveLen(1))
			Expect(published[0].Target).To(BeNil())
		})

		It("publishes once when the commit retries the modify closure", func() {
			modifyRuns = 3

			Expect(writer.WriteResult(ctx, retryCapTask("claude"))).To(Succeed())

			Expect(published).To(HaveLen(1),
				"a retried commit re-runs the closure but must not re-publish")
		})
	})

	Context("no escalation", func() {
		It("publishes nothing when the assignee was already empty", func() {
			Expect(writer.WriteResult(ctx, retryCapTask(""))).To(Succeed())

			Expect(published).To(BeEmpty(),
				"re-writing an already-parked task must not re-ping the operator")
		})

		It("publishes nothing on a routine write below every cap", func() {
			writeTaskFile(
				"my-task.md",
				"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\n"+
					"retry_count: 0\nmax_retries: 3\nassignee: claude\n---\n## Result\nStatus: ok\n",
			)
			taskFile := lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier": "test-task-uuid-1234",
					"status":          "in_progress",
					"phase":           "execution",
					"retry_count":     0,
					"max_retries":     3,
					"assignee":        "claude",
				},
				Content: lib.TaskContent("## Result\nStatus: ok\n"),
			}

			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

			Expect(published).To(BeEmpty())
		})
	})

	Context("publish failure", func() {
		It("still commits the write and does not fail the result", func() {
			sendErr = errors.New("broker unavailable")

			Expect(writer.WriteResult(ctx, retryCapTask("claude"))).To(Succeed())

			written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			Expect(readErr).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring("previous_assignee: claude"))
			Expect(string(written)).NotTo(ContainSubstring("\nassignee: claude"))
		})
	})
})
