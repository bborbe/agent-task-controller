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
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/routing"
)

var _ = Describe("NewCompleteTaskExecutor", func() {
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
		tmpDir, err = os.MkdirTemp("", "complete-task-test-*")
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
			return os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test helper
		}
		// Wire AtomicReadModifyWriteAndCommitPush to actually call the modify func and write the file.
		// A nil modify result (idempotency skip) leaves the file untouched.
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
			if updated == nil {
				return nil
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}

		executor = command.NewCompleteTaskExecutor(
			fakeGit,
			taskDir,
			"openclaw",
			libtime.NewCurrentDateTime(),
			metrics.New(),
		)
		schemaID = cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	writeTaskFile := func(name, content string) string {
		absPath := filepath.Join(tmpDir, taskDir, name)
		Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())
		return absPath
	}

	readFile := func(absPath string) string {
		content, err := os.ReadFile(absPath) // #nosec G304 -- test helper
		Expect(err).NotTo(HaveOccurred())
		return string(content)
	}

	parseFrontmatter := func(absPath string) map[string]interface{} {
		s := readFile(absPath)
		Expect(s).To(HavePrefix("---\n"))
		rest := s[4:]
		before, _, found := strings.Cut(rest, "\n---\n")
		Expect(found).To(BeTrue())
		var fm map[string]interface{}
		Expect(yaml.Unmarshal([]byte(before), &fm)).To(Succeed())
		return fm
	}

	buildCmdObj := func(cmd task.CompleteCommand) cdb.CommandObject {
		event, err := base.ParseEvent(ctx, cmd)
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				RequestID: base.NewRequestID(),
				Operation: command.CompleteTaskCommandOperation,
				Initiator: "test",
				Data:      event,
			},
			SchemaID: schemaID,
		}
	}

	Describe("CommandOperation", func() {
		It("returns complete-task", func() {
			Expect(
				executor.CommandOperation(),
			).To(Equal(base.CommandOperation("complete-task")))
		})
	})

	Describe("HandleCommand", func() {
		Context("task resolution", func() {
			It("closes the task file matching task_identifier", func() {
				const recovery = "0123456789abcdef0123456789abcdef01234567"
				taskFile := writeTaskFile(
					"build-failure.md",
					"---\ntask_identifier: complete-test-uuid\nstatus: next\n---\nbody\n",
				)
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("complete-test-uuid"),
						RecoverySHA:    recovery,
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				fm := parseFrontmatter(taskFile)
				Expect(fm["status"]).To(Equal("completed"))
				Expect(fm["phase"]).To(Equal("done"))
				Expect(fm["recovery_sha"]).To(Equal(recovery))
				Expect(fm["completed_date"]).NotTo(BeEmpty())

				content := readFile(taskFile)
				Expect(content).To(ContainSubstring("## Resolution"))
				Expect(content).To(ContainSubstring("build recovered"))
				Expect(content).To(ContainSubstring(recovery))
			})

			It("skips without writing when no matching task exists", func() {
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("nonexistent-uuid"),
					}),
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("idempotency", func() {
			It("does not append a second Resolution block on re-delivery", func() {
				taskFile := writeTaskFile(
					"build-failure.md",
					"---\ntask_identifier: idem-uuid\nstatus: next\n---\nbody\n",
				)
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("idem-uuid"),
						RecoverySHA:    "0123456789abcdef0123456789abcdef01234567",
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				// Second delivery of the same closure command
				_, _, err = executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("idem-uuid"),
						RecoverySHA:    "0123456789abcdef0123456789abcdef01234567",
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				content := readFile(taskFile)
				Expect(strings.Count(content, "## Resolution")).To(Equal(1))
			})
		})

		Context("vault routing", func() {
			It(
				"skips a mismatched-vault command with zero git writes and zero not_found increments",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: cross-vault-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
					)
					before := testutil.ToFloat64(
						metrics.FrontmatterCommandsTotal.WithLabelValues(
							"complete-task",
							"not_found",
						),
					)
					cmd := buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("cross-vault-uuid"),
						TargetVault:    "personal",
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
					// guard fires before the task-file lookup and before any write
					Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
					Expect(
						testutil.ToFloat64(
							metrics.FrontmatterCommandsTotal.WithLabelValues(
								"complete-task",
								"not_found",
							),
						),
					).To(Equal(before))
					// file untouched
					fm := parseFrontmatter(taskFile)
					Expect(fm["status"]).To(Equal("in_progress"))
					_, hasTarget := fm["target_vault"]
					Expect(hasTarget).To(BeFalse())
				},
			)

			It("processes an empty-vault command (legacy fall-through)", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: legacy-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				cmd := buildCmdObj(task.CompleteCommand{
					TaskIdentifier: lib.TaskIdentifier("legacy-uuid"),
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				fm := parseFrontmatter(taskFile)
				Expect(fm["status"]).To(Equal("completed"))
			})

			It(
				"heals a file lacking target_vault, stamping the controller vault in the same write",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: heal-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
					)
					cmd := buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("heal-uuid"),
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(err).NotTo(HaveOccurred())
					fm := parseFrontmatter(taskFile)
					Expect(fm["target_vault"]).To(Equal("openclaw"))
					// the stamped file read back through ShouldProcessResult no longer
					// falls through to the non-owning controller
					req := lib.Task{
						TaskIdentifier: lib.TaskIdentifier("heal-uuid"),
						Frontmatter: lib.TaskFrontmatter{
							"target_vault": "openclaw",
						},
					}
					Expect(routing.ShouldProcessResult(req, "personal")).To(BeFalse())
					Expect(routing.ShouldProcessResult(req, "openclaw")).To(BeTrue())
				},
			)

			It("never overrides an existing target_vault", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: stamped-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: personal\n---\nbody\n",
				)
				cmd := buildCmdObj(task.CompleteCommand{
					TaskIdentifier: lib.TaskIdentifier("stamped-uuid"),
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["target_vault"]).To(Equal("personal"))
				Expect(fm["status"]).To(Equal("completed"))
			})
		})
	})
})
