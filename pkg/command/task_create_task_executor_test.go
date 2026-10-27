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
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

const testK = 7

var _ = Describe("NewCreateTaskExecutor", func() {
	var (
		ctx       context.Context
		tmpDir    string
		taskDir   string
		fakeGit   *mocks.GitClient
		fakeVault *mocks.VaultAPIClient
		executor  cdb.CommandObjectExecutorTx
		schemaID  cdb.SchemaID
		clock     *libtimemocks.CurrentDateTimeGetter
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

		fakeVault = newFSTaskClient(tmpDir, taskDir)
		executor = command.NewCreateTaskExecutor(
			fakeGit, fakeVault, taskDir, "openclaw", clock, testK, nil,
		)
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

	// seedOccupant writes the occupant the create will find at the title path.
	// The create reads that page through the API, so a fakeGit read stub no
	// longer reaches it — the occupant has to exist on disk.
	seedOccupant := func(title string, fm map[string]any) {
		marshaled, err := yaml.Marshal(fm)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, taskDir, title+".md"),
			[]byte("---\n"+string(marshaled)+"---\nbody\n"),
			0600,
		)).To(Succeed())
	}

	// seedRaw writes an arbitrary occupant: unparseable bytes, no status key, or
	// a custom body the create must not carry over.
	seedRaw := func(title, content string) {
		Expect(os.WriteFile(
			filepath.Join(tmpDir, taskDir, title+".md"),
			[]byte(content),
			0600,
		)).To(Succeed())
	}

	// writtenTask returns the bytes the create left at the title path.
	writtenTask := func(title string) string {
		raw, err := os.ReadFile(filepath.Join(tmpDir, taskDir, title+".md"))
		Expect(err).NotTo(HaveOccurred())
		return string(raw)
	}

	// taskExists reports whether the create materialized a file at the title path.
	taskExists := func(title string) bool {
		_, err := os.Stat(filepath.Join(tmpDir, taskDir, title+".md"))
		return err == nil
	}

	// stubReadFileFromDisk makes the fake git client read seeded files from
	// tmpDir instead of the default 404 stub, so a code path that verifies an
	// on-disk occupant (rather than trusting the resolver) sees real content.
	stubReadFileFromDisk := func(git *mocks.GitClient, root string) {
		git.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			content, err := os.ReadFile(filepath.Join(root, relPath))
			if err != nil {
				return nil, errors.New("GET " + relPath + " returned 404: not found")
			}
			return content, nil
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(0))
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(0))
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(0))
			})
		})

		Context("title path already occupied (collision)", func() {
			It("returns ErrTaskAlreadyExists and leaves the file untouched (AC2)", func() {
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("replay-task"),
					Title:          "Replay Task",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})

				// First create: nothing at the title path → the API materializes it.
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				first := writtenTask("Replay Task")

				// Replay: the file the first create left behind is a live occupant
				// with the same identifier → benign already-exists, no overwrite.
				_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(writtenTask("Replay Task")).To(Equal(first))
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
			})
		})

		Context("collision with a different task_identifier", func() {
			It("disambiguates the path with a short-identifier suffix and writes (AC4)", func() {
				// Existing file at the title path belongs to a DIFFERENT task and its
				// status "todo" is non-terminal — a two-identifiers-one-title-path
				// filename collision. The losing identifier must still get its own
				// file, never be orphaned.
				seedOccupant("My Colliding Task", map[string]any{
					"task_identifier": "someone-else",
					"assignee":        "alice",
					"status":          "todo",
				})
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("new-task-id"),
					Title:          "My Colliding Task",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(taskExists("My Colliding Task - new-task")).To(BeTrue())
			})
		})

		Context("identifier already has a file under a different title (regression)", func() {
			It(
				"ExistingIdentifierTitleIsReused: posts the create under the existing file's title stem",
				func() {
					const existingID = "09f1f349-bf09-5610-9cf2-bb79cf0383be"
					seedRaw(
						"Existing Title",
						"---\ntask_identifier: "+existingID+"\nassignee: alice\nstatus: in_progress\n---\nbody\n",
					)
					stubReadFileFromDisk(fakeGit, tmpDir)
					fakeResolver := &mocks.TaskPathResolver{}
					fakeResolver.ResolveReturns("tasks/Existing Title.md", true, nil)
					redirectingExecutor := command.NewCreateTaskExecutor(
						fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier(existingID),
						Title:          "A Different Title",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})

					_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
					_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
					Expect(posted.Title).To(Equal("Existing Title"))
					Expect(taskExists("A Different Title")).To(BeFalse())
					Expect(
						writtenTask("Existing Title"),
					).To(ContainSubstring("status: in_progress"))
				},
			)

			It(
				"StaleIndexDoesNotOverwriteAnotherTerminalTask: a resolved file carrying a DIFFERENT identifier is not redirected onto",
				func() {
					// The index is not self-freshening: idX -> "Renamed Away.md" can
					// survive a rewrite that made that file carry idA instead. The
					// server replaces ANY terminal occupant regardless of identifier,
					// so redirecting would destroy idA's completed record.
					const staleID = "aaaaaaaa-1111-5111-8111-111111111111"
					const occupantID = "bbbbbbbb-2222-5222-8222-222222222222"
					seedRaw(
						"Renamed Away",
						"---\ntask_identifier: "+occupantID+"\nassignee: alice\nstatus: completed\n---\nbody\n",
					)
					stubReadFileFromDisk(fakeGit, tmpDir)
					fakeResolver := &mocks.TaskPathResolver{}
					fakeResolver.ResolveReturns("tasks/Renamed Away.md", true, nil)
					redirectingExecutor := command.NewCreateTaskExecutor(
						fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier(staleID),
						Title:          "My Own Title",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})

					_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())

					// The redirect did not fire: the create used the command's title.
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
					_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
					Expect(posted.Title).To(Equal("My Own Title"))
					// The other task's completed record is byte-identical.
					Expect(writtenTask("Renamed Away")).To(Equal(
						"---\ntask_identifier: " + occupantID + "\nassignee: alice\nstatus: completed\n---\nbody\n",
					))
				},
			)

			It(
				"UnreadableResolvedFileIsNotRedirectedOnto: a read failure falls back to the command title",
				func() {
					// Cannot verify the occupant => cannot redirect: an unverified
					// redirect is the overwrite path itself.
					fakeGit.ReadFileReturns(nil, errors.New("GET tasks/Gone.md returned 500: boom"))
					fakeResolver := &mocks.TaskPathResolver{}
					fakeResolver.ResolveReturns("tasks/Gone.md", true, nil)
					redirectingExecutor := command.NewCreateTaskExecutor(
						fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("cccccccc-3333-5333-8333-333333333333"),
						Title:          "Own Title Again",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})

					_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
					_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
					Expect(posted.Title).To(Equal("Own Title Again"))
				},
			)

			It("does not redirect when the resolver reports an ambiguous identifier", func() {
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns(
					"",
					false,
					errors.New("duplicate: tasks/a.md, tasks/b.md"),
				)
				redirectingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("ambiguous-id"),
					Title:          "Own Title",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})

				_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
				Expect(posted.Title).To(Equal("Own Title"))
				Expect(taskExists("Own Title")).To(BeTrue())
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
					title := "PR Review github - bborbe-run - 20 - a52b3fa4 - update-go-module-dependencies"
					first := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("3aa3f6ba-e9bb-5276-b2bf-274624c90d93"),
						Title:          title,
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, first)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))

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
					Expect(taskExists(title)).To(BeTrue())
					Expect(taskExists(title + " - 1dfcfe48")).To(BeTrue())
				},
			)
		})

		Context("both the title path and its disambiguated sibling are occupied", func() {
			It("maps the second conflict to ErrTaskAlreadyExists, not a generic failure", func() {
				// A replay of a disambiguated task: the title path still belongs to
				// the other task, and the suffixed path now holds THIS task's own
				// file. Result-topic consumers special-case ErrTaskAlreadyExists, so
				// the second 409 must carry the same sentinel as the first.
				seedRaw(
					"Alt Occupied",
					"---\ntask_identifier: someone-else\nassignee: alice\nstatus: next\n---\nbody\n",
				)
				seedRaw(
					"Alt Occupied - alt-task",
					"---\ntask_identifier: alt-task-id\nassignee: claude\nstatus: next\n---\nbody\n",
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier("alt-task-id"),
					Title:          "Alt Occupied",
					Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
			})
		})

		Context("transient API error", func() {
			// The occupant read moved server-side with the typed surface, so the
			// controller can no longer distinguish "unreadable" from "absent" — the
			// API answers, and anything that is not a conflict must propagate
			// untouched rather than be mistaken for a benign duplicate.
			It("propagates a non-conflict error and writes nothing (AC5)", func() {
				fakeVault.CreateTaskReturns(
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
				Expect(taskExists("Flaky")).To(BeFalse())
			})
		})

		Context("terminal status completed frees the title path (reopen)", func() {
			It(
				"materializes a fresh non-terminal task over the completed file (AC terminal-completed)",
				func() {
					seedRaw("Reopen Completed",
						"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\n"+
							"phase: done\ncompleted_date: 2026-06-01T10:00:00Z\n---\n"+
							"premature triage verdict for this alert\n")
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("reopen-completed"),
						Title:          "Reopen Completed",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))

					// Content equals a first-ever create — nothing carried over from the terminal file.
					contentStr := writtenTask("Reopen Completed")
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
					seedRaw("Reopen Aborted",
						"---\ntask_identifier: old-id\nassignee: alice\nstatus: aborted\n"+
							"phase: done\n---\nprior verdict body\n")
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("reopen-aborted"),
						Title:          "Reopen Aborted",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				},
			)
		})

		Context("recurring-task instance never reopens a terminal file", func() {
			It(
				"holds the title path over a completed file (recurring dedup contract)",
				func() {
					seedRaw("Monthly Recurring - 2026-08",
						"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\n"+
							"phase: done\ncompleted_date: 2026-08-15T12:00:00Z\n---\n"+
							"completed monthly backup work\n")
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
					Expect(writtenTask("Monthly Recurring - 2026-08")).To(
						ContainSubstring("status: completed"),
					)
				},
			)

			It(
				"holds the title path over an aborted file (recurring dedup contract)",
				func() {
					seedRaw("Weekly Recurring - 2026W35",
						"---\ntask_identifier: old-id\nassignee: alice\nstatus: aborted\n"+
							"phase: done\n---\nprior body\n")
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
					Expect(writtenTask("Weekly Recurring - 2026W35")).To(
						ContainSubstring("status: aborted"),
					)
				},
			)

			It(
				"reopens over a completed file when the command is not recurring",
				func() {
					seedRaw("Analyze Alert",
						"---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\n"+
							"phase: done\n---\nper-alert prior verdict\n")
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
					Expect(writtenTask("Analyze Alert")).NotTo(
						ContainSubstring("per-alert prior verdict"),
					)
				},
			)
		})

		Context("conflict carrying no identifier", func() {
			It(
				"maps it to task.ErrTaskAlreadyExists without disambiguating",
				func() {
					// A conflict that names no identifier cannot prove the occupant is a
					// DIFFERENT task, so disambiguating would orphan a duplicate of this
					// one. It has to fail closed — the same answer for an unparseable
					// occupant, which is what the API sends in this shape.
					fakeVault.CreateTaskReturns(nil, &vaultapi.AlreadyExistsError{
						Code:   "task_exists",
						Status: "",
					})
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("anonymous-collision"),
						Title:          "Anonymous Collision",
						Frontmatter: lib.TaskFrontmatter{
							"assignee": "claude",
							"status":   "next",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
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
					seedRaw("Non Terminal",
						"---\ntask_identifier: non-terminal\nassignee: alice\nstatus: "+
							existingStatus+"\n---\nbody\n")
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("non-terminal"),
						Title:          "Non Terminal",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(writtenTask("Non Terminal")).To(
						ContainSubstring("status: " + existingStatus),
					)
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
					seedRaw("Ambiguous", string(existing))
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("ambiguous"),
						Title:          "Ambiguous",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(writtenTask("Ambiguous")).To(Equal(string(existing)))
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
					// First-ever create: the slot is free.
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					content0 := writtenTask("Byte Identical")

					// Reopen: same command, but the slot now holds a terminal file.
					seedRaw("Byte Identical", "---\nstatus: aborted\n---\nprior verdict body\n")
					_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())

					Expect(writtenTask("Byte Identical")).To(Equal(content0))
				},
			)
		})

		Context("reopen then replay is idempotent", func() {
			It(
				"reopens a terminal file once, then holds the path on replay (AC replay-idempotency)",
				func() {
					seedRaw("Replay Idempotent",
						"---\ntask_identifier: replay-old\nassignee: alice\nstatus: completed\n"+
							"---\nbody\n")
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("replay-idempotent"),
						Title:          "Replay Idempotent",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})
					// First handle: terminal file → reopen → replaced.
					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					reopened := writtenTask("Replay Idempotent")
					Expect(reopened).To(ContainSubstring("status: next"))

					// Replay: the just-written non-terminal file holds the path.
					_, _, err = executor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).To(HaveOccurred())
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					// unchanged across replay
					Expect(writtenTask("Replay Idempotent")).To(Equal(reopened))
				},
			)
		})

		// The reopen-vs-create commit message is no longer the controller's to
		// build: the typed surface commits inside vault-api, which pins that
		// message in its own suite. What remains observable here — the file being
		// replaced rather than merged — is covered by the reopen specs above.

		Context("success: new file created", func() {
			It("writes the expected content at the title path", func() {
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
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))

				contentStr := writtenTask("New Task ABC")
				Expect(contentStr).To(HavePrefix("---\n"))
				Expect(strings.Count(contentStr, "---")).To(BeNumerically(">=", 2))
				Expect(contentStr).To(ContainSubstring("task_identifier: " + string(taskID)))
				Expect(contentStr).To(ContainSubstring("assignee: claude"))
				Expect(contentStr).To(ContainSubstring("status: next"))
				Expect(contentStr).To(ContainSubstring("target_vault: openclaw"))
				Expect(contentStr).To(ContainSubstring("This is the task body."))
			})
		})

		Context("create write fails", func() {
			It("returns the error and leaves no file behind", func() {
				fakeVault.CreateTaskReturns(nil, errors.New("git push failed"))

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
				Expect(taskExists("Error Task")).To(BeFalse())
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
				Expect(taskExists("My Feature Task")).To(BeTrue())
				Expect(taskExists(string(taskID))).To(BeFalse())
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
				Expect(taskExists(string(taskID))).To(BeTrue())
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
				Expect(taskExists(string(taskID))).To(BeTrue())
			})
		})

		Context("index recording", func() {
			const createdID = "44444444-4444-4444-8444-444444444444"

			createCmd := func() cdb.CommandObject {
				return buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier(createdID),
					Title:          "Recorded Task",
					Frontmatter: lib.TaskFrontmatter{
						"assignee": "claude",
						"status":   "next",
					},
				})
			}

			It("records the written path on a clean miss", func() {
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns("", false, nil)
				recordingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)

				_, _, err := recordingExecutor.HandleCommand(ctx, nil, createCmd())
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeResolver.RecordCallCount()).To(Equal(1))
				_, id, relPath := fakeResolver.RecordArgsForCall(0)
				Expect(id).To(Equal(lib.TaskIdentifier(createdID)))
				Expect(relPath).To(Equal(filepath.Join(taskDir, "Recorded Task.md")))
			})

			It("does not record when the index already holds the identifier", func() {
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns("tasks/other.md", true, nil)
				recordingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)

				_, _, err := recordingExecutor.HandleCommand(ctx, nil, createCmd())
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeResolver.RecordCallCount()).To(Equal(0))
				// The index hit points at a file that does not carry this
				// identifier, so the redirect must not fire: the create posts under
				// the command's own title, not "other".
				_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
				Expect(posted.Title).To(Equal("Recorded Task"))
			})

			It("does not record when the resolver reports a duplicate", func() {
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns("", false, errors.New("duplicate"))
				recordingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)

				_, _, err := recordingExecutor.HandleCommand(ctx, nil, createCmd())
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeResolver.RecordCallCount()).To(Equal(0))
			})

			It("does not resolve or record when the create fails as already existing", func() {
				fakeResolver := &mocks.TaskPathResolver{}
				seedRaw("Recorded Task",
					"---\ntask_identifier: "+createdID+"\nstatus: in_progress\n---\nbody\n")
				recordingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)

				_, _, err := recordingExecutor.HandleCommand(ctx, nil, createCmd())
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())

				// One resolve chooses the create title; none is made for recording
				// because the create failed as already-existing.
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				Expect(fakeResolver.RecordCallCount()).To(Equal(0))
			})

			It(
				"falls back to the identifier for a title the command rejects, and records it",
				func() {
					// createTaskTitle falls back to the identifier when cmd.Validate
					// fails, and that validation already rejects a ".." title — so no
					// title reaching the join can form a traversal segment, and a create
					// can never be left on disk but unindexed.
					fakeResolver := &mocks.TaskPathResolver{}
					fakeResolver.ResolveReturns("", false, nil)
					recordingExecutor := command.NewCreateTaskExecutor(
						fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
					)
					cmdObj := buildCmdObj(task.CreateCommand{
						TaskIdentifier: lib.TaskIdentifier("ellipsis-id"),
						Title:          "Wait... what?",
						Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
					})

					_, _, err := recordingExecutor.HandleCommand(ctx, nil, cmdObj)
					Expect(err).NotTo(HaveOccurred())
					Expect(taskExists("ellipsis-id")).To(BeTrue())

					Expect(fakeResolver.RecordCallCount()).To(Equal(1))
					_, _, relPath := fakeResolver.RecordArgsForCall(0)
					Expect(relPath).To(Equal(filepath.Join(taskDir, "ellipsis-id.md")))
				},
			)

			It("records the request title's path, not the server's echoed title", func() {
				// The server may normalise the title it stores. The index and the
				// supersede back-pointers key off the path this controller asked for,
				// so a differing echo must not move the recorded path.
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns("", false, nil)
				fakeVault.CreateTaskStub = func(
					_ context.Context,
					_ vaultapi.Task,
					_ vaultapi.CreateOptions,
				) (*vaultapi.Task, error) {
					return &vaultapi.Task{ID: "server-id", Title: "server normalised title"}, nil
				}
				recordingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)

				_, _, err := recordingExecutor.HandleCommand(ctx, nil, createCmd())
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeResolver.RecordCallCount()).To(Equal(1))
				_, _, relPath := fakeResolver.RecordArgsForCall(0)
				Expect(relPath).To(Equal(filepath.Join(taskDir, "Recorded Task.md")))
			})
		})

		Context("vault routing", func() {
			It(
				"skips a command whose TargetVault is openclaw when vaultName=personal (ErrCommandObjectSkipped, no git write)",
				func() {
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						fakeVault,
						taskDir,
						"personal",
						clock,
						testK,
						nil,
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
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(0))
				},
			)

			It(
				"processes a command whose TargetVault is openclaw when vaultName=openclaw (one git write)",
				func() {
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						fakeVault,
						taskDir,
						"openclaw",
						clock,
						testK,
						nil,
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
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				},
			)

			It(
				"processes a command with empty TargetVault when vaultName=openclaw (legacy fallback)",
				func() {
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						fakeVault,
						taskDir,
						"openclaw",
						clock,
						testK,
						nil,
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
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				},
			)

			It(
				"skips a command with empty TargetVault when vaultName=personal (legacy fallback is openclaw, not personal)",
				func() {
					executor := command.NewCreateTaskExecutor(
						fakeGit,
						fakeVault,
						taskDir,
						"personal",
						clock,
						testK,
						nil,
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
					Expect(fakeVault.CreateTaskCallCount()).To(Equal(0))
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
						fakeVault.CreateTaskCallCount(),
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
					fakeVault,
					taskDir,
					"openclaw",
					clock,
					smallK,
					nil,
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
				// No collision-check read any more: the create answers that itself.
				// 2 candidate reads (k=2) = 2 total.
				Expect(fakeGit.ReadFileCallCount()).To(Equal(2))
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

		Context("build-fix supersede retirement", func() {
			buildFixCmd := func(id lib.TaskIdentifier, fm lib.TaskFrontmatter) cdb.CommandObject {
				return buildCmdObj(task.CreateCommand{
					TaskIdentifier: id,
					Title:          "Fix Build - dashboard",
					Frontmatter:    fm,
				})
			}

			It("retires the other live task for the same build_id", func() {
				priorPath := "tasks/Fix Build - dashboard - b1file.md"
				newPath := "tasks/Fix Build - dashboard.md"
				priorContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\nphase: execution\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{priorPath, newPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == priorPath {
						return priorContent, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"phase":               "execution",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				_, absPath, modify, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(priorPath))
				Expect(msg).To(ContainSubstring("auto-supersede build-fix:"))

				retired, modifyErr := modify(priorContent)
				Expect(modifyErr).NotTo(HaveOccurred())
				retiredStr := string(retired)
				Expect(retiredStr).To(ContainSubstring("status: aborted"))
				Expect(retiredStr).To(ContainSubstring("phase: done"))
				Expect(retiredStr).To(ContainSubstring("completed_date:"))
				Expect(retiredStr).To(ContainSubstring("superseded_by:"))
				Expect(retiredStr).To(ContainSubstring(newPath))
				Expect(retiredStr).NotTo(ContainSubstring("created_by: recurring-task-creator"))

				Expect(writtenTask("Fix Build - dashboard")).To(
					ContainSubstring("status: in_progress"),
				)
			})

			It("retires the task whose task_identifier matches supersedes_task_id", func() {
				priorPath := "tasks/Fix Build - dashboard - b1file.md"
				unrelatedPath := "tasks/Unrelated Task.md"
				priorContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B1\nassignee: claude\nstatus: in_progress\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{priorPath, unrelatedPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					switch relPath {
					case priorPath:
						return priorContent, nil
					case unrelatedPath:
						return []byte(
							"---\ntask_identifier: unrelated-1\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":           "claude",
					"status":             "in_progress",
					"build_id":           "B2",
					"supersedes_task_id": "fixbuild-b1",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				_, absPath, modify, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(priorPath))
				Expect(msg).To(ContainSubstring("auto-supersede build-fix:"))

				retired, modifyErr := modify(priorContent)
				Expect(modifyErr).NotTo(HaveOccurred())
				retiredStr := string(retired)
				Expect(retiredStr).To(ContainSubstring("status: aborted"))
				Expect(retiredStr).To(ContainSubstring("phase: done"))
				Expect(retiredStr).To(ContainSubstring("completed_date:"))
				Expect(retiredStr).To(ContainSubstring("superseded_by:"))
				Expect(retiredStr).NotTo(ContainSubstring("created_by: recurring-task-creator"))
			})

			DescribeTable(
				"writes nothing when the markers are absent or empty",
				func(fm lib.TaskFrontmatter) {
					priorPath := "tasks/Prior Build Fix.md"
					priorContent := []byte(
						"---\ntask_identifier: prior-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
					)
					Expect(
						os.WriteFile(filepath.Join(tmpDir, priorPath), priorContent, 0600),
					).To(Succeed())
					fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
						_ context.Context,
						absPath string,
						modify func([]byte) ([]byte, error),
						_ string,
					) error {
						current, readErr := os.ReadFile(absPath)
						if readErr != nil {
							return readErr
						}
						updated, modifyErr := modify(current)
						if modifyErr != nil {
							return modifyErr
						}
						return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
					}

					_, _, err := executor.HandleCommand(
						ctx,
						nil,
						buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), fm),
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))

					after, readErr := os.ReadFile(filepath.Join(tmpDir, priorPath))
					Expect(readErr).NotTo(HaveOccurred())
					Expect(after).To(Equal(priorContent))
				},
				Entry("no marker keys", lib.TaskFrontmatter{
					"assignee": "claude",
					"status":   "in_progress",
					"build_id": "B",
				}),
				Entry("markers present but empty", lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_task_id":  "",
					"supersedes_build_id": "",
				}),
			)

			It(
				"creates no second file and retires nothing when the same build is re-emitted unchanged",
				func() {
					existing := "---\ntask_identifier: fixbuild-b2\nbuild_id: B\n" +
						"assignee: claude\nstatus: in_progress\n---\nbody\n"
					seedRaw("Fix Build - dashboard", existing)

					cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
						"assignee":            "claude",
						"status":              "in_progress",
						"build_id":            "B",
						"supersedes_build_id": "B",
					})

					_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
					Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
					Expect(writtenTask("Fix Build - dashboard")).To(Equal(existing))
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
				},
			)

			It("does not retire the task it has just created", func() {
				// The create now lands on disk through the API, so the retire scan
				// reads the real file — the one it must recognise as itself.
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					raw, readErr := os.ReadFile(filepath.Join(tmpDir, relPath))
					if readErr != nil {
						return nil, errors.New("GET " + relPath + " returned 404: not found")
					}
					return raw, nil
				}
				fakeGit.ListFilesReturns([]string{"tasks/Fix Build - dashboard.md"}, nil)

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when the supersede target is unreadable", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return nil, errors.New("git-rest 503")
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when the supersede target is unparseable", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte("---\nstatus: [unclosed\n---\nbody\n"), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("does not retire a target that is already terminal", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				terminalContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: aborted\nphase: done\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return terminalContent, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when listing candidates fails", func() {
				fakeGit.ListFilesReturns(nil, errors.New("git-rest 503"))

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when the retire write is rejected", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}
				fakeGit.AtomicReadModifyWriteAndCommitPushReturns(errors.New("git-rest 409"))

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
			})

			It("treats a path-traversal marker as an inert value, never a path", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":           "claude",
					"status":             "in_progress",
					"build_id":           "B",
					"supersedes_task_id": "../../etc/passwd",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				// The glob is the only path the marker could have influenced, and it is
				// constant: the marker is compared as a value, never joined into a path.
				Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
				_, glob := fakeGit.ListFilesArgsForCall(0)
				Expect(glob).To(Equal("tasks/*.md"))
				// The candidate is live and carries build_id B, but the command's
				// supersedes_build_id is absent and its supersedes_task_id does not
				// match fixbuild-b1 — so nothing is retired.
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("retires a task-marker match and a build-marker match in one pass", func() {
				byTaskID := "tasks/Fix Build - dashboard - byid.md"
				byBuildID := "tasks/Fix Build - dashboard - bybuild.md"
				fakeGit.ListFilesReturns([]string{byTaskID, byBuildID}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					switch relPath {
					case byTaskID:
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: OTHER\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					case byBuildID:
						return []byte(
							"---\ntask_identifier: unrelated-1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_task_id":  "fixbuild-b1",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				// Neither candidate matches both markers: the two predicates are
				// evaluated independently, so each retires exactly one file.
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
			})
		})
	})
})
