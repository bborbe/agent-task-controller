// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// This file owns the identifier-redirect cases of the create executor: a create
// whose task_identifier already has a file is posted under THAT file's title, so
// the record store stays one-file-per-identifier. It lives apart from
// task_create_task_executor_test.go, which carries the title-path, reopen and
// supersede cases and sits at the repo's 2000-line limit.
var _ = Describe("NewCreateTaskExecutor identifier redirect", func() {
	var (
		ctx       context.Context
		tmpDir    string
		taskDir   string
		fakeGit   *mocks.GitClient
		fakeVault *mocks.VaultAPIClient
		clock     *libtimemocks.CurrentDateTimeGetter
		schemaID  cdb.SchemaID
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "create-task-redirect-test-*")
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
	// tmpDir instead of the default 404 stub, so the verify-before-redirect read
	// sees real content.
	stubReadFileFromDisk := func(git *mocks.GitClient, root string) {
		git.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			content, err := os.ReadFile(filepath.Join(root, relPath))
			if err != nil {
				return nil, errors.New("GET " + relPath + " returned 404: not found")
			}
			return content, nil
		}
	}

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

		It(
			"RecurringRedirectStaysOneFile: a recurring re-emit of a terminal instance does not create a second file",
			func() {
				// The redirect lands on the file that carries this identifier, but a
				// recurring command sends ReopenTerminal=false, so the server answers
				// 409 with the SAME identifier and the executor maps that to the benign
				// already-exists — never the ` - <id[:8]>` disambiguation that would
				// leave two files sharing one identifier.
				const instanceID = "dddddddd-4444-5444-8444-444444444444"
				seedRaw(
					"Monthly Recurring - 2026-08",
					"---\ntask_identifier: "+instanceID+"\nassignee: alice\nstatus: completed\n"+
						"created_by: recurring-task-creator\n---\nbody\n",
				)
				stubReadFileFromDisk(fakeGit, tmpDir)
				fakeResolver := &mocks.TaskPathResolver{}
				fakeResolver.ResolveReturns("tasks/Monthly Recurring - 2026-08.md", true, nil)
				redirectingExecutor := command.NewCreateTaskExecutor(
					fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
				)
				cmdObj := buildCmdObj(task.CreateCommand{
					TaskIdentifier: lib.TaskIdentifier(instanceID),
					Title:          "Monthly Recurring - 2026-09",
					Frontmatter: lib.TaskFrontmatter{
						"assignee":   "claude",
						"status":     "next",
						"created_by": "recurring-task-creator",
					},
				})

				_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(taskExists("Monthly Recurring - 2026-09")).To(BeFalse())
				Expect(
					taskExists("Monthly Recurring - 2026-09 - " + instanceID[:8]),
				).To(BeFalse())
				Expect(writtenTask("Monthly Recurring - 2026-08")).To(
					ContainSubstring("status: completed"),
				)
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
})
