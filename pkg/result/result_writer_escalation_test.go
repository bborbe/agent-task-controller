// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	lib "github.com/bborbe/agent"
	notifcore "github.com/bborbe/notification"
	notifcmd "github.com/bborbe/notification/command/notification"
	"github.com/bborbe/run"
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
			var modifyErr error
			for i := 0; i < modifyRuns; i++ {
				updated, modifyErr = modify(current)
				if modifyErr != nil {
					return modifyErr
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

	// prTaskFile builds a retry-cap task file whose *name* is the coalescing input and
	// returns the lib.Task that targets it. Distinct task files for one PR carry distinct
	// task_identifier values and distinct short SHAs — exactly the shape the retry path
	// mints in production, where --force appends " - retry-<taskid[:8]>" and a new head SHA
	// produces its own file.
	prTaskFile := func(taskName string, id lib.TaskIdentifier) lib.Task {
		writeTaskFile(
			taskName+".md",
			"---\ntask_identifier: "+string(id)+"\nstatus: in_progress\nphase: execution\n"+
				"retry_count: 3\nmax_retries: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
		)
		return lib.Task{
			TaskIdentifier: id,
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": string(id),
				"status":          "in_progress",
				"phase":           "execution",
				"retry_count":     3,
				"max_retries":     3,
				"assignee":        "claude",
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

	Context("escalation notification coalescing (spec 013)", func() {
		base := libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))

		It("coalesces a repeat escalation of one PR and still parks both files", func() {
			firstName := "PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - " +
				"feat-publish-go-release-notification"
			secondName := "PR Review github - bborbe-go-version-watcher - 15 - 2d12d9f5 - " +
				"feat-publish-go-release-notification"
			first := prTaskFile(
				firstName,
				lib.TaskIdentifier("11111111-1111-5111-8111-111111111111"),
			)
			second := prTaskFile(
				secondName,
				lib.TaskIdentifier("22222222-2222-5222-8222-222222222222"),
			)

			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(writer.WriteResult(ctx, second)).To(Succeed())

			Expect(published).To(HaveLen(1),
				"the second task file for the same PR must not re-ping")

			for _, name := range []string{firstName, secondName} {
				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, name+".md"))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(written)).To(ContainSubstring("previous_assignee: claude"))
				Expect(string(written)).NotTo(ContainSubstring("\nassignee: claude"))
			}
			Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2),
				"coalescing drops the ping, never the write")
		})

		It("does not coalesce the same PR number in a different repository", func() {
			first := prTaskFile(
				"PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("33333333-3333-5333-8333-333333333331"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-beta - 10 - bbbb2222 - fix-thing",
				lib.TaskIdentifier("33333333-3333-5333-8333-333333333332"),
			)

			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(writer.WriteResult(ctx, second)).To(Succeed())

			Expect(published).To(HaveLen(2))
		})

		It("does not coalesce a different PR number in the same repository", func() {
			first := prTaskFile(
				"PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("44444444-4444-5444-8444-444444444441"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-alpha - 11 - bbbb2222 - fix-thing",
				lib.TaskIdentifier("44444444-4444-5444-8444-444444444442"),
			)

			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(writer.WriteResult(ctx, second)).To(Succeed())

			Expect(published).To(HaveLen(2))
		})

		It("does not coalesce two repositories carrying the same short SHA", func() {
			first := prTaskFile(
				"PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("55555555-5555-5555-8555-555555555551"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-beta - 11 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("55555555-5555-5555-8555-555555555552"),
			)

			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(writer.WriteResult(ctx, second)).To(Succeed())

			Expect(published).To(HaveLen(2))
		})

		It("suppresses inside the window and publishes at the boundary", func() {
			first := prTaskFile(
				"PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-release",
				lib.TaskIdentifier("66666666-6666-5666-8666-666666666661"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-go-version-watcher - 15 - 2d12d9f5 - feat-release",
				lib.TaskIdentifier("66666666-6666-5666-8666-666666666662"),
			)
			third := prTaskFile(
				"PR Review github - bborbe-go-version-watcher - 15 - 5e33e5ff - feat-release",
				lib.TaskIdentifier("66666666-6666-5666-8666-666666666663"),
			)

			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(published).To(HaveLen(1))

			fakeTime.NowReturns(base.Add(libtime.Duration(29 * time.Minute)))
			Expect(writer.WriteResult(ctx, second)).To(Succeed())
			Expect(published).To(HaveLen(1), "the same key at T+29min must be suppressed")

			fakeTime.NowReturns(base.Add(libtime.Duration(30 * time.Minute)))
			Expect(writer.WriteResult(ctx, third)).To(Succeed())
			Expect(published).To(HaveLen(2), "three escalations, two publishes")
		})

		It("publishes uncoalesced for a task name the parse does not recognise", func() {
			first := prTaskFile(
				"Build Failure github - bborbe-agent - deadbeef",
				lib.TaskIdentifier("77777777-7777-5777-8777-777777777771"),
			)
			Expect(writer.WriteResult(ctx, first)).To(Succeed())
			Expect(published).To(HaveLen(1))

			second := prTaskFile(
				"Update Go bborbe-vault-cli f9b19bd",
				lib.TaskIdentifier("77777777-7777-5777-8777-777777777772"),
			)
			Expect(writer.WriteResult(ctx, second)).To(Succeed())
			Expect(published).To(HaveLen(2))

			third := prTaskFile(
				"PR Review github - bborbe-other - 99 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("77777777-7777-5777-8777-777777777773"),
			)
			Expect(writer.WriteResult(ctx, third)).To(Succeed())
			Expect(published).To(HaveLen(3))

			fourth := prTaskFile(
				"Dark Factory Implement github - bborbe-agent - "+
					"PR Review github - bborbe-other - 99 - aaaa1111",
				lib.TaskIdentifier("77777777-7777-5777-8777-777777777774"),
			)
			Expect(writer.WriteResult(ctx, fourth)).To(Succeed())
			Expect(published).To(HaveLen(4),
				"the parse is anchored: a PR-shaped fragment later in the name must not move the key")

			fifth := prTaskFile(
				"Build Failure github - bborbe-agent - deadbeef",
				lib.TaskIdentifier("77777777-7777-5777-8777-777777777775"),
			)
			Expect(writer.WriteResult(ctx, fifth)).To(Succeed())
			Expect(published).To(HaveLen(5), "an unmatched name creates no window state")
		})

		It("does not consume the window when the send fails", func() {
			first := prTaskFile(
				"PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-release",
				lib.TaskIdentifier("88888888-8888-5888-8888-888888888881"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-go-version-watcher - 15 - 2d12d9f5 - feat-release",
				lib.TaskIdentifier("88888888-8888-5888-8888-888888888882"),
			)

			sendErr = errors.New("broker unavailable")
			Expect(writer.WriteResult(ctx, first)).To(Succeed())

			sendErr = nil
			Expect(writer.WriteResult(ctx, second)).To(Succeed())

			Expect(published).To(HaveLen(2),
				"a rejected send must not record the key")
		})

		It("publishes once for two concurrent escalations of one key", func() {
			first := prTaskFile(
				"PR Review github - bborbe-agent - 9 - aaaa1111 - fix-thing",
				lib.TaskIdentifier("99999999-9999-5999-8999-999999999991"),
			)
			second := prTaskFile(
				"PR Review github - bborbe-agent - 9 - bbbb2222 - fix-thing",
				lib.TaskIdentifier("99999999-9999-5999-8999-999999999992"),
			)

			var publishMutex sync.Mutex
			var concurrentPublished []notifcmd.NotificationPublishCommand
			concurrentWriter := result.NewResultWriter(
				fakeGit,
				taskDir,
				"openclaw",
				fakeTime,
				metrics.New(),
				libtime.NewWaiterDuration(),
				notifcmd.NotificationPublishCommandSenderFunc(
					func(_ context.Context, command notifcmd.NotificationPublishCommand) error {
						publishMutex.Lock()
						defer publishMutex.Unlock()
						concurrentPublished = append(concurrentPublished, command)
						return nil
					},
				),
			)

			Expect(run.CancelOnFirstErrorWait(
				ctx,
				func(runCtx context.Context) error {
					return concurrentWriter.WriteResult(runCtx, first)
				},
				func(runCtx context.Context) error {
					return concurrentWriter.WriteResult(runCtx, second)
				},
			)).To(Succeed())

			Expect(concurrentPublished).To(HaveLen(1))
		})
	})
})
