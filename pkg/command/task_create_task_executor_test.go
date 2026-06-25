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

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
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

		executor = command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw")
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
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "personal")
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
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw")
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
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw")
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
					executor := command.NewCreateTaskExecutor(fakeGit, taskDir, "personal")
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
	})
})
