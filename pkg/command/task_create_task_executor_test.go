// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("NewCreateTaskExecutor", func() {
	var (
		ctx      context.Context
		tmpDir   string
		taskDir  string
		fakeGit  *mocks.GitClient
		executor cdb.CommandObjectExecutorTx
		schemaID cdb.SchemaID
		clock    *libtimemocks.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "create-task-test-*")
		Expect(err).NotTo(HaveOccurred())

		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.AtomicWriteAndCommitPushStub = func(
			ctx context.Context,
			absPath string,
			content []byte,
			message string,
		) error {
			return os.WriteFile(absPath, content, 0600) // #nosec G306 -- test helper
		}
		// Default: every title path is free unless a test overrides ReadFile.
		fakeGit.ReadFileReturns(nil, errors.New("GET file returned 404: not found"))

		clock = &libtimemocks.CurrentDateTimeGetter{}
		clock.NowReturns(libtime.DateTime(time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)))

		executor = command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock)
		schemaID = cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	buildCmdObj := func(cmd task.CreateCommand) cdb.CommandObject {
		event, err := base.ParseEvent(ctx, cmd)
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				RequestID: base.NewRequestID(),
				Operation: task.CreateCommandOperation,
				Initiator: "test",
				Data:      event,
			},
			SchemaID: schemaID,
		}
	}

	Describe("CommandOperation", func() {
		It("returns create-task", func() {
			Expect(executor.CommandOperation()).To(Equal(base.CommandOperation("create-task")))
		})
	})

	Describe("HandleCommand", func() {
		Context("malformed command payload", func() {
			It("returns ErrCommandObjectSkipped without writing", func() {
				// A channel is not JSON-marshalable, so MarshalInto will fail.
				cmdObj := cdb.CommandObject{
					Command: base.Command{
						RequestID: base.NewRequestID(),
						Operation: task.CreateCommandOperation,
						Initiator: "test",
						Data:      base.Event{"taskIdentifier": make(chan int)},
					},
					SchemaID: schemaID,
				}
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("empty TaskIdentifier", func() {
			It("returns a validation error without writing", func() {
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier(""),
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("missing assignee in frontmatter", func() {
			It("returns a validation error without writing", func() {
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("my-task-id"),
					Frontmatter: lib.TaskFrontmatter{
						"status": "next",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("assignee"))
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("missing status in frontmatter", func() {
			It("returns a validation error without writing", func() {
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("my-task-id"),
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("status"))
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("title path already occupied (collision)", func() {
			It("returns ErrTaskAlreadyExists and does not write (AC2)", func() {
				// Second ReadFile call returns existing content → collision on replay.
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET 24 Tasks/Replay Task.md returned 404: not found"))
				fakeGit.ReadFileReturnsOnCall(
					1,
					[]byte(
						"---\ntask_identifier: replay-task\nassignee: claude\nstatus: next\n---\n",
					),
					nil,
				)

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("replay-task"),
					Title:          "Replay Task",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})

				// First create: file not found → writes.
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))

				// Replay: file now exists → sentinel, no second write.
				_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1)) // still 1
			})
		})

		Context("new filename", func() {
			It("writes exactly once and returns nil when ReadFile reports not-found (AC3)", func() {
				fakeGit.ReadFileReturns(
					nil,
					errors.New("GET 24 Tasks/Brand New.md returned 404: not found"),
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("brand-new"),
					Title:          "Brand New",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
			})
		})

		Context("collision with a different task_identifier", func() {
			It("returns ErrTaskAlreadyExists and does not write (AC4)", func() {
				// Existing file at the title path belongs to a DIFFERENT task — filename owns the
				// slot; the executor must not consult frontmatter, must not write.
				fakeGit.ReadFileReturns(
					[]byte(
						"---\ntask_identifier: someone-else\nassignee: alice\nstatus: todo\n---\n",
					),
					nil,
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("new-task-id"),
					Title:          "My Colliding Task",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("transient git-rest read error", func() {
			It("propagates the wrapped error and does not write (AC5)", func() {
				fakeGit.ReadFileReturns(
					nil,
					errors.New("GET 24 Tasks/Flaky.md returned 503: service unavailable"),
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("flaky-task"),
					Title:          "Flaky",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeFalse())
				Expect(err.Error()).To(ContainSubstring("503"))
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("success: new file created", func() {
			It("calls AtomicWriteAndCommitPush with correct content and commit message", func() {
				taskID := lib.TaskIdentifier("new-task-abc")
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: taskID,
					Title:          "New Task ABC",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
					Body: "This is the task body.\n",
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))

				_, absPath, content, message := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("New Task ABC.md"))
				Expect(message).To(ContainSubstring(string(taskID)))

				contentStr := string(content)
				Expect(contentStr).To(HavePrefix("---\n"))
				Expect(strings.Count(contentStr, "---")).To(BeNumerically(">=", 2))
				Expect(contentStr).To(ContainSubstring("task_identifier:"))
				Expect(contentStr).To(ContainSubstring("assignee:"))
				Expect(contentStr).To(ContainSubstring("status:"))
				Expect(contentStr).To(ContainSubstring("This is the task body."))
			})
		})

		Context("git write error", func() {
			It("returns a wrapped error when AtomicWriteAndCommitPush fails", func() {
				fakeGit.AtomicWriteAndCommitPushStub = nil
				fakeGit.AtomicWriteAndCommitPushReturns(errors.New("git push failed"))

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("error-task"),
					Title:          "Error Task",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("git push failed"))
			})
		})

		Context("valid title", func() {
			It("writes the task file at tasks/{title}.md", func() {
				taskID := lib.TaskIdentifier("uuid-1234")
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: taskID,
					Title:          "My Feature Task",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
					Body: "Task description.\n",
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("My Feature Task.md"))
				Expect(absPath).NotTo(ContainSubstring(string(taskID)))
			})
		})

		Context("invalid title (contains forbidden char)", func() {
			It("logs WARN and writes the task file at tasks/{task_identifier}.md", func() {
				taskID := lib.TaskIdentifier("uuid-5678")
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: taskID,
					Title:          "bad/title",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(string(taskID) + ".md"))
			})
		})

		Context("empty title", func() {
			It("logs WARN and writes the task file at tasks/{task_identifier}.md", func() {
				taskID := lib.TaskIdentifier("uuid-empty-title")
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: taskID,
					Title:          "",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(string(taskID) + ".md"))
			})
		})

		Context("vault routing", func() {
			It(
				"skips a command whose TargetVault is openclaw when vaultName=personal (no git write, no error)",
				func() {
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "personal", clock)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-1"),
						Title:          "Personal Task",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
						TargetVault: "openclaw",
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It(
				"processes a command whose TargetVault is openclaw when vaultName=openclaw (one git write)",
				func() {
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-1"),
						Title:          "Openclaw Task",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
						TargetVault: "openclaw",
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				},
			)

			It(
				"processes a command with empty TargetVault when vaultName=openclaw (legacy fallback)",
				func() {
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-1"),
						Title:          "Legacy Task",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
						// TargetVault deliberately empty — legacy producer.
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				},
			)

			It(
				"skips a command with empty TargetVault when vaultName=personal (legacy fallback is openclaw, not personal)",
				func() {
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "personal", clock)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-1"),
						Title:          "Legacy Task",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
						// TargetVault deliberately empty.
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)
		})
		Context("supersede prior recurring task", func() {
			// Helper: prior-file content for in_progress status.
			priorInProgressContent := []byte(
				"---\ntask_identifier: prior-id\nassignee: claude\nstatus: in_progress\n---\nbody text\n",
			)

			It("AC supersede happy path: aborts prior in_progress instance", func() {
				// Call 0: title-collision check → 404 free.
				// Call 1: prior-file read → in_progress content.
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))
				fakeGit.ReadFileReturnsOnCall(1, priorInProgressContent, nil)

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27"),
					Title:          "Aquascape PWC - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())

				// AtomicWriteAndCommitPush for new instance.
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				// Prior-file read occurred.
				Expect(fakeGit.ReadFileCallCount()).To(Equal(2))
				// AtomicReadModifyWriteAndCommitPush called to abort prior.
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				// Capture modify args.
				_, absPath, modifyFn, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(
					0,
				)
				Expect(absPath).To(HaveSuffix("Aquascape PWC - 2026W26.md"))
				Expect(msg).To(ContainSubstring("auto-supersede prior recurring task"))

				// Run the modify closure on prior content and verify result.
				resultBytes, modErr := modifyFn([]byte(priorInProgressContent))
				Expect(modErr).NotTo(HaveOccurred())
				Expect(string(resultBytes)).To(ContainSubstring("status: aborted"))
				Expect(string(resultBytes)).To(ContainSubstring("phase: done"))
				Expect(string(resultBytes)).To(ContainSubstring("completed_date:"))
				Expect(
					string(resultBytes),
				).To(ContainSubstring("superseded_by: tasks/Aquascape PWC - 2026W27.md"))
				Expect(
					string(resultBytes),
				).To(ContainSubstring("created_by: recurring-task-creator"))
			})

			It(
				"AC auto_abort_prior absent: skips prior-file read entirely (opt-in required)",
				func() {
					// Only title-collision check occurs; no prior-file ReadFile.
					// auto_abort_prior is absent - opt-in not set, so no supersede.
					fakeGit.ReadFileReturnsOnCall(0,
						nil, errors.New("GET tasks returned 404: not found"))

					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-w27-audit"),
						Title:          "check-prometheus-alerts - 2026W27",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "recurring-task-creator",
							// auto_abort_prior intentionally absent - opt-in required
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
					Expect(fakeGit.ReadFileCallCount()).To(Equal(1)) // only collision check
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It("AC not created by publisher: skips prior-file read entirely", func() {
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27-manual"),
					Title:          "Manual Task - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
						// no created_by field
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.ReadFileCallCount()).To(Equal(1)) // only collision check
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("AC prior not found: no AtomicReadModifyWriteAndCommitPush", func() {
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))
				// Prior file also 404.
				fakeGit.ReadFileReturnsOnCall(1,
					nil, errors.New("GET tasks/Aquascape PWC - 2026W26.md returned 404: not found"))

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27-first"),
					Title:          "Aquascape PWC - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("AC prior status completed: no AtomicReadModifyWriteAndCommitPush", func() {
				priorCompletedContent := []byte(
					"---\ntask_identifier: prior-id\nassignee: claude\nstatus: completed\n---\nbody text\n",
				)
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))
				fakeGit.ReadFileReturnsOnCall(1, priorCompletedContent, nil)

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27"),
					Title:          "Aquascape PWC - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It(
				"AC prior status aborted (redelivery 2nd pass): no AtomicReadModifyWriteAndCommitPush",
				func() {
					priorAbortedContent := []byte(
						"---\ntask_identifier: prior-id\nassignee: claude\nstatus: aborted\nphase: done\n---\nbody text\n",
					)
					fakeGit.ReadFileReturnsOnCall(0,
						nil, errors.New("GET tasks returned 404: not found"))
					fakeGit.ReadFileReturnsOnCall(1, priorAbortedContent, nil)

					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-w27"),
						Title:          "Aquascape PWC - 2026W27",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "recurring-task-creator",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It("AC prior read non-404 error swallowed: handler returns nil error", func() {
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))
				// Transient server error on prior-file read.
				fakeGit.ReadFileReturnsOnCall(
					1,
					nil,
					errors.New("GET tasks/Aquascape PWC - 2026W26.md returned 500: server error"),
				)

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27"),
					Title:          "Aquascape PWC - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				ret0, ret1, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(ret0).To(BeNil())
				Expect(ret1).To(BeNil())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It(
				"AC unrecognized title (no period token): skips prior-file read beyond collision check",
				func() {
					fakeGit.ReadFileReturnsOnCall(0,
						nil, errors.New("GET tasks returned 404: not found"))

					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("task-random"),
						Title:          "My Random Task",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "recurring-task-creator",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeGit.ReadFileCallCount()).To(Equal(1)) // only collision check
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It("AC supersede write failure swallowed: handler returns nil error", func() {
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))
				fakeGit.ReadFileReturnsOnCall(1, priorInProgressContent, nil)
				fakeGit.AtomicReadModifyWriteAndCommitPushReturns(
					errors.New("git-rest 503"))

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27"),
					Title:          "Aquascape PWC - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				ret0, ret1, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(ret0).To(BeNil())
				Expect(ret1).To(BeNil())
			})

			It("AC path-separator guard: skips when prior title contains separator", func() {
				// The slug already contains a path separator — this would be rejected by
				// resolveCreateTaskRelPath for a new write (falls back to UUID path),
				// but the supersede path-traversal guard catches it at the decrement step.
				fakeGit.ReadFileReturnsOnCall(0,
					nil, errors.New("GET tasks returned 404: not found"))

				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("task-w27-slash"),
					Title:          "Reports/Weekly - 2026W27",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())

				// Prior file never read (guard fires before read).
				Expect(fakeGit.ReadFileCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})
	})
})
