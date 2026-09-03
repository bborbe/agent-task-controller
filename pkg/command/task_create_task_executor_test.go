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
	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
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
		fakeGit.AtomicWriteIfAbsentAndCommitPushStub = func(
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

		Context("empty assignee in frontmatter (task born parked)", func() {
			// An empty assignee is the operator-inbox signal, not a defect:
			// github-pr-watcher's untrusted-author path creates tasks with
			// `assignee: "", phase: human_review` so a human picks them up.
			// Requiring assignee here silently dropped every such task.
			It("creates the task without an assignee", func() {
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("my-task-id"),
					Frontmatter: lib.TaskFrontmatter{
						"status": "todo",
						"phase":  "human_review",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
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

				// First create: file not found → writes via create-only.
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))

				// Replay: file now exists → sentinel, no second write.
				_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1)) // still 1
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
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
			})
		})

		Context("collision with a different task_identifier", func() {
			It("disambiguates the path with a short-identifier suffix and writes (AC4)", func() {
				// Existing file at the title path belongs to a DIFFERENT task and its
				// status "todo" is non-terminal — a two-identifiers-one-title-path
				// filename collision. The losing identifier must still get its own
				// file, never be orphaned.
				fakeGit.ReadFileReturnsOnCall(
					0,
					[]byte(
						"---\ntask_identifier: someone-else\nassignee: alice\nstatus: todo\n---\n",
					),
					nil,
				)
				fakeGit.ReadFileReturnsOnCall(
					1,
					nil,
					errors.New("GET file returned 404: not found"),
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("new-task-id"),
					Title:          "My Colliding Task",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				_, statErr := os.Stat(filepath.Join(
					tmpDir, "tasks", "My Colliding Task - new-task.md",
				))
				Expect(statErr).NotTo(HaveOccurred())
			})
		})

		Context("two identifiers targeting one title path (regression)", func() {
			It(
				"materializes both tasks — the loser at a disambiguated path, never dropped",
				func() {
					// Reproduces the prod incident (2026-09-02): the watcher's canonical
					// identifier claims the title path, then 27ms later a second,
					// different identifier claims the SAME title. The loser must get
					// its own file so its results resolve — the pre-fix behavior
					// orphaned it and dropped every result forever.
					fakeGit.ReadFileReturnsOnCall(
						0,
						nil,
						errors.New("GET file returned 404: not found"),
					)
					fakeGit.ReadFileReturnsOnCall(
						1,
						[]byte(
							"---\ntask_identifier: 3aa3f6ba-e9bb-5276-b2bf-274624c90d93\nassignee: claude\nstatus: next\n---\n",
						),
						nil,
					)
					fakeGit.ReadFileReturnsOnCall(
						2,
						nil,
						errors.New("GET file returned 404: not found"),
					)

					title := "PR Review github - bborbe-run - 20 - a52b3fa4 - update-go-module-dependencies"
					first := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("3aa3f6ba-e9bb-5276-b2bf-274624c90d93"),
						Title:          title,
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, first)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))

					second := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("1dfcfe48-e957-f2a8-c7a2-e9b8bc083a25"),
						Title:          title,
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "pr-reviewer-agent",
							"status":   "next",
						},
					})
					_, _, err = executor.HandleCommand(ctx, nil, second)
					Expect(err).NotTo(HaveOccurred())

					// Both identifiers resolve to real files: the canonical title path
					// and the disambiguated short-id path.
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(2))
					_, statErr := os.Stat(filepath.Join(tmpDir, "tasks", title+".md"))
					Expect(statErr).NotTo(HaveOccurred())
					_, statErr = os.Stat(filepath.Join(
						tmpDir, "tasks", title+" - 1dfcfe48.md",
					))
					Expect(statErr).NotTo(HaveOccurred())
				},
			)
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

		Context("terminal status completed frees the title path (reopen)", func() {
			It(
				"materializes a fresh non-terminal task over the completed file (AC terminal-completed)",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\nphase: done\ncompleted_date: 2026-06-01T10:00:00Z\n---\npremature triage verdict for this alert\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("reopen-completed"),
						Title:          "Reopen Completed",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
					// Single-ReadFile lock: the status decision reuses the collision-check bytes.
					Expect(fakeGit.ReadFileCallCount()).To(Equal(1))

					// Content equals a first-ever create — nothing carried over from the terminal file.
					_, _, content, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
					contentStr := string(content)
					Expect(contentStr).To(ContainSubstring("status: next"))
					Expect(contentStr).NotTo(ContainSubstring("completed_date"))
					Expect(contentStr).NotTo(ContainSubstring("phase: done"))
					Expect(contentStr).NotTo(ContainSubstring("premature triage verdict"))
				},
			)
		})

		Context("terminal status aborted frees the title path (reopen)", func() {
			It(
				"materializes a fresh non-terminal task over the aborted file (AC terminal-aborted)",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old-id\nassignee: alice\nstatus: aborted\nphase: done\n---\nprior verdict body\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("reopen-aborted"),
						Title:          "Reopen Aborted",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
				},
			)
		})

		Context("recurring-task instance never reopens a terminal file", func() {
			It(
				"holds the title path over a completed file (recurring dedup contract)",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\nphase: done\ncompleted_date: 2026-08-15T12:00:00Z\n---\ncompleted monthly backup work\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("monthly-recurring"),
						Title:          "Monthly Recurring - 2026-08",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "recurring-task-creator",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It(
				"holds the title path over an aborted file (recurring dedup contract)",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old-id\nassignee: alice\nstatus: aborted\nphase: done\n---\nprior body\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("weekly-recurring"),
						Title:          "Weekly Recurring - 2026W35",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "recurring-task-creator",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It(
				"reopens over a completed file when the command is not recurring",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\nphase: done\n---\nper-alert prior verdict\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("per-alert"),
						Title:          "Analyze Alert",
						Frontmatter: lib.TaskFrontmatter{
							"assignee":   "claude",
							"status":     "next",
							"created_by": "sentry-collector-agent",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(0))
				},
			)
		})

		Context("create-only write refuses an occupied path (defense-in-depth)", func() {
			It(
				"maps git-rest ErrAlreadyExists to task.ErrTaskAlreadyExists without a second write",
				func() {
					// The pre-check read falsely reports the path free (404) while the
					// create-only write still hits the existing file — the TOCTOU the
					// create-only write closes. git-rest answers 409, surfaced as
					// ErrAlreadyExists, and the executor must treat it as a benign
					// already-exists, never overwrite.
					fakeGit.ReadFileReturns(nil, errors.New("GET file returned 404: not found"))
					fakeGit.AtomicWriteIfAbsentAndCommitPushReturns(gitrestclient.ErrAlreadyExists)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("create-only-collision"),
						Title:          "Create Only Collision",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)
		})

		Context("non-terminal status holds the title path (no reopen)", func() {
			DescribeTable(
				"returns ErrTaskAlreadyExists without writing for a SAME-identifier occupant",
				func(existingStatus string) {
					// Same task_identifier re-publish: the task already has its
					// file, so the idempotent duplicate keeps failing benignly
					// (no disambiguation — that path is for different-identifier
					// collisions, covered by the two-identifiers regression test).
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: non-terminal\nassignee: alice\nstatus: "+existingStatus+"\n---\nbody\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("non-terminal"),
						Title:          "Non Terminal",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					alreadyExists := errors.Is(err, task.ErrTaskAlreadyExists)
					Expect(alreadyExists).To(BeTrue())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(0))
				},
				Entry("next", "next"),
				Entry("in_progress", "in_progress"),
				Entry("backlog", "backlog"),
				Entry("hold", "hold"),
			)
		})

		Context("unreadable or ambiguous existing file holds the title path (fail closed)", func() {
			DescribeTable(
				"returns ErrTaskAlreadyExists without writing",
				func(existing []byte) {
					fakeGit.ReadFileReturns(existing, nil)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("ambiguous"),
						Title:          "Ambiguous",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					alreadyExists := errors.Is(err, task.ErrTaskAlreadyExists)
					Expect(alreadyExists).To(BeTrue())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
				},
				Entry("no frontmatter delimiters", []byte("plain text, no frontmatter\n")),
				Entry("syntactically invalid YAML", []byte("---\nstatus: [\n---\n")),
				Entry(
					"valid frontmatter with no status key",
					[]byte("---\nassignee: alice\n---\n"),
				),
				Entry("empty status", []byte("---\nstatus: \"\"\n---\n")),
				Entry("unknown status value", []byte("---\nstatus: some-unknown-value\n---\n")),
			)
		})

		Context("reopen content is byte-identical to a first-ever create", func() {
			It(
				"writes identical bytes whether the slot was free or freed by a terminal status (AC content-equals-first-create)",
				func() {
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("byte-identical"),
						Title:          "Byte Identical",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
						Body:           "shared body\n",
					})
					// First-ever create: default 404 read frees the slot (create-only write).
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					_, _, content0, _ := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)

					// Reopen: same command, but the slot now holds a terminal file (upsert write).
					fakeGit.ReadFileReturns(
						[]byte("---\nstatus: aborted\n---\nprior verdict body\n"),
						nil,
					)
					_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					_, _, content1, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)

					Expect(content1).To(Equal(content0))
				},
			)
		})

		Context("reopen then replay is idempotent", func() {
			It(
				"reopens a terminal file once, then holds the path on replay (AC replay-idempotency)",
				func() {
					fakeGit.ReadFileReturnsOnCall(
						0,
						[]byte(
							"---\ntask_identifier: replay-old\nassignee: alice\nstatus: completed\n---\nbody\n",
						),
						nil,
					)
					fakeGit.ReadFileReturnsOnCall(
						1,
						[]byte("---\nstatus: next\n---\n"),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("replay-idempotent"),
						Title:          "Replay Idempotent",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					// First handle: terminal file → reopen → write.
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(1))
					// Replay: just-written non-terminal content → hold path.
					_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					alreadyExists := errors.Is(err, task.ErrTaskAlreadyExists)
					Expect(alreadyExists).To(BeTrue())
					Expect(
						fakeGit.AtomicWriteAndCommitPushCallCount(),
					).To(Equal(1))
					// unchanged across replay
				},
			)
		})

		Context("commit message distinguishes reopen from first-ever create", func() {
			It(
				"commits a reopen terminal task message when the slot was freed by a terminal status",
				func() {
					fakeGit.ReadFileReturns(
						[]byte(
							"---\ntask_identifier: old\nassignee: alice\nstatus: completed\nphase: done\n---\nprior verdict\n",
						),
						nil,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("reopen-msg"),
						Title:          "Reopen Msg",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					_, _, _, message := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)
					Expect(message).To(ContainSubstring("reopen terminal task"))
				},
			)

			It(
				"keeps the create task message on a first-ever create (no reopen substring)",
				func() {
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("first-create-msg"),
						Title:          "First Create Msg",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					_, _, _, message := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)
					Expect(message).NotTo(ContainSubstring("reopen terminal task"))
					Expect(message).To(ContainSubstring("create task"))
				},
			)
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
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))

				_, absPath, content, message := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(
					0,
				)
				Expect(absPath).To(HaveSuffix("New Task ABC.md"))
				Expect(message).To(ContainSubstring(string(taskID)))

				contentStr := string(content)
				Expect(contentStr).To(HavePrefix("---\n"))
				Expect(strings.Count(contentStr, "---")).To(BeNumerically(">=", 2))
				Expect(contentStr).To(ContainSubstring("task_identifier:"))
				Expect(contentStr).To(ContainSubstring("assignee:"))
				Expect(contentStr).To(ContainSubstring("status:"))
				Expect(contentStr).To(ContainSubstring("target_vault: openclaw"))
				Expect(contentStr).To(ContainSubstring("This is the task body."))
			})
		})

		Context("git write error", func() {
			It("returns a wrapped error when AtomicWriteIfAbsentAndCommitPush fails", func() {
				fakeGit.AtomicWriteIfAbsentAndCommitPushReturns(errors.New("git push failed"))

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
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)
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
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)
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
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				_, absPath, _, _ := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(string(taskID) + ".md"))
			})
		})

		Context("vault routing", func() {
			It(
				"skips a command whose TargetVault is openclaw when vaultName=personal (ErrCommandObjectSkipped, no git write)",
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
					// ErrCommandObjectSkipped — the result-sender wrapper (production)
					// converts it to a silent skip (no Success result published).
					Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
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
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
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
					Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
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
					Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
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
						fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount(),
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
