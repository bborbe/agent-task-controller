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

const testK = 7

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

		executor = command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock, testK)
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
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						taskDir,
						"personal",
						clock,
						testK,
					)
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
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						taskDir,
						"openclaw",
						clock,
						testK,
					)
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
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						taskDir,
						"openclaw",
						clock,
						testK,
					)
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
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						taskDir,
						"personal",
						clock,
						testK,
					)
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

		Context("scan-and-collapse supersede", func() {
			inProgress := func(id string) []byte {
				return []byte(
					"---\ntask_identifier: " + id + "\nassignee: claude\nstatus: in_progress\n---\nbody\n",
				)
			}

			// AC: N-collapse — missed-day gap (Mon+Tue open, Wed fires → both close, Wed stays open)
			It(
				"closes multiple older in_progress priors when new instance fires (missed-day gap)",
				func() {
					newTitle := "IBKR Swing Trading - 2026W28-wed"
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("ibkr-w28wed"),
						Title:          newTitle,
						Frontmatter: lib.TaskFrontmatter{
							"assignee":         "claude",
							"status":           "next",
							"created_by":       "recurring-task-creator",
							"auto_abort_prior": true,
						},
					})

					candidatePaths := []string{
						"tasks/IBKR Swing Trading - 2026W28-mon.md",
						"tasks/IBKR Swing Trading - 2026W28-tue.md",
					}
					fakeGit.ListFilesReturns(candidatePaths, nil)
					monContent := inProgress("ibkr-w28mon")
					tueContent := inProgress("ibkr-w28tue")
					fileContents := map[string][]byte{
						"tasks/IBKR Swing Trading - 2026W28-mon.md": monContent,
						"tasks/IBKR Swing Trading - 2026W28-tue.md": tueContent,
					}
					fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
						if content, ok := fileContents[relPath]; ok {
							return content, nil
						}
						return nil, errors.New("GET " + relPath + " returned 404: not found")
					}

					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(
						fakeGit.AtomicWriteAndCommitPushCallCount(),
					).To(Equal(1))
					// new instance only
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))

					// Ranking sorts by ordinal desc; equal ordinals sort by Title desc.
					// "2026W28-tue" > "2026W28-mon" alphabetically, so tue is first.
					priorContents := [][]byte{tueContent, monContent}
					priorSuffixes := []string{"tue", "mon"}
					for i := 0; i < 2; i++ {
						_, absPath, modify, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(
							i,
						)
						Expect(msg).To(ContainSubstring("auto-supersede prior recurring task"))
						resultContent, parseErr := modify(priorContents[i])
						Expect(parseErr).NotTo(HaveOccurred())
						resultStr := string(resultContent)
						Expect(resultStr).To(ContainSubstring("status: aborted"))
						Expect(resultStr).To(ContainSubstring("phase: done"))
						Expect(resultStr).To(ContainSubstring("completed_date:"))
						Expect(resultStr).To(ContainSubstring("superseded_by:"))
						Expect(resultStr).To(ContainSubstring("created_by: recurring-task-creator"))
						Expect(
							absPath,
						).To(HaveSuffix("IBKR Swing Trading - 2026W28-" + priorSuffixes[i] + ".md"))
					}
				},
			)

			// AC: weekday-set-agnostic — sparse mon/wed/fri set, same week ordinal
			It("closes equal-ordinal same-week siblings (sparse weekday set)", func() {
				newTitle := "Sched - 2026W28-fri"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("sched-w28fri"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				candidatePaths := []string{
					"tasks/Sched - 2026W28-mon.md",
					"tasks/Sched - 2026W28-wed.md",
				}
				fakeGit.ListFilesReturns(candidatePaths, nil)
				fileContents := map[string][]byte{
					"tasks/Sched - 2026W28-mon.md": inProgress("sched-w28mon"),
					"tasks/Sched - 2026W28-wed.md": inProgress("sched-w28wed"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
			})

			// AC: look-back cap with small K
			It("honors look-back bound k and only closes the k most-recent candidates", func() {
				smallK := 2
				localExecutor := command.NewCreateTaskExecutor(
					fakeGit,
					taskDir,
					"openclaw",
					clock,
					smallK,
				)

				newTitle := "Weekly Sched - 2026W30"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w30"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				candidatePaths := []string{
					"tasks/Weekly Sched - 2026W29.md",
					"tasks/Weekly Sched - 2026W28.md",
					"tasks/Weekly Sched - 2026W27.md",
					"tasks/Weekly Sched - 2026W26.md",
				}
				fakeGit.ListFilesReturns(candidatePaths, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2026W29.md": inProgress("weekly-w29"),
					"tasks/Weekly Sched - 2026W28.md": inProgress("weekly-w28"),
					"tasks/Weekly Sched - 2026W27.md": inProgress("weekly-w27"),
					"tasks/Weekly Sched - 2026W26.md": inProgress("weekly-w26"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := localExecutor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				// 1 collision-check read + 2 candidate reads (k=2) = 3 total
				Expect(fakeGit.ReadFileCallCount()).To(Equal(3))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))

				// Most recent two (W29, W28) are closed; W27, W26 left open
				var closedPaths []string
				for i := 0; i < 2; i++ {
					_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(i)
					closedPaths = append(closedPaths, absPath)
				}
				Expect(closedPaths[0]).To(HaveSuffix("Weekly Sched - 2026W29.md"))
				Expect(closedPaths[1]).To(HaveSuffix("Weekly Sched - 2026W28.md"))
			})

			// AC: cross-year ISO-week ranking
			It("ranks correctly across ISO-week and year boundary", func() {
				newTitle := "Weekly Sched - 2026W01"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w0101"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				candidatePaths := []string{
					"tasks/Weekly Sched - 2025W52.md",
					"tasks/Weekly Sched - 2025W51.md",
				}
				fakeGit.ListFilesReturns(candidatePaths, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2025W52.md": inProgress("weekly-w52"),
					"tasks/Weekly Sched - 2025W51.md": inProgress("weekly-w51"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))

				// W52 is more recent than W51, so first abort should be W52
				_, firstPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(firstPath).To(HaveSuffix("Weekly Sched - 2025W52.md"))
			})

			// AC: Daily regression — collapse to one
			It("closes one older daily prior", func() {
				newTitle := "Cleanup - 2026-06-15"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("cleanup-0615"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Cleanup - 2026-06-14.md"}, nil)
				fileContents := map[string][]byte{
					"tasks/Cleanup - 2026-06-14.md": inProgress("cleanup-0614"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("Cleanup - 2026-06-14.md"))
			})

			// AC: Weekly regression — collapse to one
			It("closes one older weekly prior", func() {
				newTitle := "Aquascape PWC - 2026W27"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("aqua-w27"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Aquascape PWC - 2026W26.md"}, nil)
				fileContents := map[string][]byte{
					"tasks/Aquascape PWC - 2026W26.md": inProgress("aqua-w26"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("Aquascape PWC - 2026W26.md"))
			})

			// AC: idempotency — prior already aborted
			It("skips already-aborted prior (Kafka redelivery idempotency)", func() {
				newTitle := "Weekly Sched - 2026W27"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w27idemp"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Weekly Sched - 2026W26.md"}, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2026W26.md": []byte(
						"---\ntask_identifier: weekly-w26\nassignee: claude\nstatus: aborted\n---\nbody\n",
					),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			// AC: not eligible — auto_abort_prior absent
			It("returns before listing when auto_abort_prior is absent", func() {
				newTitle := "Weekly Sched - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w28noabort"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":   "claude",
						"status":     "next",
						"created_by": "recurring-task-creator",
						// auto_abort_prior intentionally absent
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Weekly Sched - 2026W27.md"}, nil)

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			// AC: ListFiles error swallowed
			It("returns (nil,nil,nil) when ListFiles fails", func() {
				newTitle := "Weekly Sched - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w28listerr"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns(nil, errors.New("git-rest 503"))

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			// AC: per-candidate read error swallowed, others processed
			It("still closes eligible candidates when one candidate read fails", func() {
				newTitle := "Weekly Sched - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w28readerr"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{
					"tasks/Weekly Sched - 2026W27.md",
					"tasks/Weekly Sched - 2026W26.md",
				}, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2026W26.md": inProgress("weekly-w26"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					if strings.Contains(relPath, "2026W27") {
						return nil, errors.New("GET " + relPath + " returned 500")
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("Weekly Sched - 2026W26.md"))
			})

			// AC: write error swallowed
			It("returns (nil,nil,nil) when write of prior fails", func() {
				newTitle := "Weekly Sched - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w28writeerr"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Weekly Sched - 2026W27.md"}, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2026W27.md": inProgress("weekly-w27"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}
				fakeGit.AtomicReadModifyWriteAndCommitPushReturns(errors.New("git-rest 503"))

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
			})

			// AC: glob-safety fallback — slug with glob metacharacters
			It("falls back to list-all when slug contains glob metacharacters", func() {
				newTitle := "Report [draft] - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("report-draft-w28"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{"tasks/Report [draft] - 2026W27.md"}, nil)
				fileContents := map[string][]byte{
					"tasks/Report [draft] - 2026W27.md": inProgress("report-draft-w27"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
				_, globArg := fakeGit.ListFilesArgsForCall(0)
				Expect(globArg).To(Equal("tasks/*.md")) // list-all fallback, not slug-scoped
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("Report [draft] - 2026W27.md"))
			})

			// AC: unrelated-slug filtered out
			It("does not read or close candidates with a different slug", func() {
				newTitle := "Weekly Sched - 2026W28"
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("weekly-w28filter"),
					Title:          newTitle,
					Frontmatter: lib.TaskFrontmatter{
						"assignee":         "claude",
						"status":           "next",
						"created_by":       "recurring-task-creator",
						"auto_abort_prior": true,
					},
				})

				fakeGit.ListFilesReturns([]string{
					"tasks/Weekly Sched - 2026W27.md",
					"tasks/Other Sched - 2026W27.md",
				}, nil)
				fileContents := map[string][]byte{
					"tasks/Weekly Sched - 2026W27.md": inProgress("weekly-w27"),
					"tasks/Other Sched - 2026W27.md":  inProgress("other-w27"),
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if content, ok := fileContents[relPath]; ok {
						return content, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix("Weekly Sched - 2026W27.md"))
			})
		})
	})
})
