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
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("NewCompleteTaskExecutor", func() {
	var (
		ctx          context.Context
		tmpDir       string
		taskDir      string
		fakeGit      *mocks.GitClient
		fakeVault    *mocks.VaultAPIClient
		fakeResolver *mocks.TaskPathResolver
		executor     cdb.CommandObjectExecutorTx
		schemaID     cdb.SchemaID
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

		// Default miss: every existing spec keeps the walk-only behaviour it had.
		fakeResolver = &mocks.TaskPathResolver{}
		fakeResolver.ResolveReturns("", false, nil)

		fakeVault = newFSTaskClient(tmpDir, taskDir)
		executor = command.NewCompleteTaskExecutor(
			fakeVault,
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

		Context("identifier index", func() {
			It("resolves an indexed identifier with zero listings and one read", func() {
				const uuid = "aaaaaaaa-1111-4111-8111-111111111111"
				taskFile := writeTaskFile(
					"index-hit.md",
					"---\ntask_identifier: "+uuid+"\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				fakeResolver.ResolveReturns("tasks/index-hit.md", true, nil)

				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier(uuid),
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				// The identifier IS the address now: no path resolution and no
				// directory listing, because there is no vault to walk. Two reads,
				// not one: the body half re-reads immediately before its PUT so a
				// concurrent body edit is not overwritten.
				Expect(fakeVault.ReadTaskCallCount()).To(Equal(2))
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
				Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
				Expect(parseFrontmatter(taskFile)["status"]).To(Equal("completed"))
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

		Context("failure modes", func() {
			It("surfaces a PATCH failure and leaves the task open", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: patchfail-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				fakeVault.PatchTaskReturns(nil, errors.New("patch rejected"))
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("patchfail-uuid"),
					}),
				)
				Expect(err).To(HaveOccurred())
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
				Expect(parseFrontmatter(taskFile)["status"]).To(Equal("in_progress"))
			})

			It("leaves the task completed with no Resolution when the PUT fails", func() {
				// The documented partial-application window: the guard reads the
				// BODY, so a retry still writes — which is what makes this
				// recoverable, and why the guard must not read the status.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: putfail-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				fakeVault.PutBodyReturns(errors.New("body write failed"))
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("putfail-uuid"),
					}),
				)
				Expect(err).To(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))

				fm := parseFrontmatter(taskFile)
				Expect(fm["status"]).To(Equal("completed"))
				Expect(fm["phase"]).To(Equal("done"))
				Expect(readFile(taskFile)).NotTo(ContainSubstring("## Resolution"))
			})

			It("recovers on re-delivery after a partial write, appending one Resolution", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: recover-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				cmd := task.CompleteCommand{TaskIdentifier: lib.TaskIdentifier("recover-uuid")}

				// First delivery: the PATCH lands, the PUT does not.
				// PutBodyReturns nils the stub, so capture the filesystem-backed
				// one first and restore it for the retry.
				realPut := fakeVault.PutBodyStub
				fakeVault.PutBodyReturns(errors.New("body write failed"))
				_, _, err := executor.HandleCommand(ctx, nil, buildCmdObj(cmd))
				Expect(err).To(HaveOccurred())

				// Second delivery: the retry re-runs both halves and appends once.
				fakeVault.PutBodyStub = realPut
				_, _, err = executor.HandleCommand(ctx, nil, buildCmdObj(cmd))
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.PatchTaskCallCount()).To(Equal(2))
				content := readFile(taskFile)
				Expect(strings.Count(content, "## Resolution")).To(Equal(1))
				Expect(parseFrontmatter(taskFile)["status"]).To(Equal("completed"))
			})

			It("merges the Resolution onto the re-read body, not the first read", func() {
				// Pins WHICH body the section is merged onto: swapping
				// resolutionBody(fresh.Body, …) back to resolutionBody(found.Body, …)
				// must fail this. The first read returns the older body; the
				// re-read sees the operator's section and must preserve it.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: reread-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n\n## Operator\n\nkeep me\n",
				)
				realRead := fakeVault.ReadTaskStub
				calls := 0
				fakeVault.ReadTaskStub = func(
					c context.Context,
					id string,
				) (*vaultapi.Task, error) {
					found, err := realRead(c, id)
					if err != nil {
						return nil, err
					}
					calls++
					if calls == 1 {
						stale := *found
						stale.Body = "body\n"
						return &stale, nil
					}
					return found, nil
				}

				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("reread-uuid"),
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.ReadTaskCallCount()).To(Equal(2))
				content := readFile(taskFile)
				Expect(content).To(ContainSubstring("## Operator"))
				Expect(content).To(ContainSubstring("keep me"))
				Expect(strings.Count(content, "## Resolution")).To(Equal(1))
			})

			It("surfaces a re-read failure after the PATCH landed", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: rereadfail-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				before := testutil.ToFloat64(
					metrics.FrontmatterCommandsTotal.WithLabelValues("complete-task", "error"),
				)
				realRead := fakeVault.ReadTaskStub
				calls := 0
				fakeVault.ReadTaskStub = func(
					c context.Context,
					id string,
				) (*vaultapi.Task, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("vault-api unreachable")
					}
					return realRead(c, id)
				}

				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("rereadfail-uuid"),
					}),
				)
				Expect(err).To(HaveOccurred())
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.FrontmatterCommandsTotal.WithLabelValues(
							"complete-task",
							"error",
						),
					),
				).To(Equal(before + 1))
				// the PATCH landed, the body did not: the documented partial state
				Expect(parseFrontmatter(taskFile)["status"]).To(Equal("completed"))
				Expect(readFile(taskFile)).NotTo(ContainSubstring("## Resolution"))
			})

			It("skips the body write when a concurrent close already resolved it", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: raced-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				realRead := fakeVault.ReadTaskStub
				calls := 0
				fakeVault.ReadTaskStub = func(
					c context.Context,
					id string,
				) (*vaultapi.Task, error) {
					found, err := realRead(c, id)
					if err != nil {
						return nil, err
					}
					calls++
					if calls == 2 {
						// the concurrent writer's close, visible only on the re-read
						raced := *found
						raced.Body = found.Body + "\n## Resolution\n\n- closed by someone else\n"
						return &raced, nil
					}
					return found, nil
				}

				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("raced-uuid"),
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
				Expect(strings.Count(readFile(taskFile), "## Resolution")).To(Equal(0))
			})

			It("addresses the body PUT by the re-read task's id, not the stale one", func() {
				// The filesystem fake gives both reads the same ID, so without a
				// stub that makes them differ, reverting fresh.ID to found.ID
				// leaves the suite green — the hardening would be untested.
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: idmix-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				realRead := fakeVault.ReadTaskStub
				calls := 0
				fakeVault.ReadTaskStub = func(
					c context.Context,
					id string,
				) (*vaultapi.Task, error) {
					found, err := realRead(c, id)
					if err != nil {
						return nil, err
					}
					calls++
					if calls == 1 {
						stale := *found
						stale.ID = "stale-id"
						return &stale, nil
					}
					fresh := *found
					fresh.ID = "fresh-id"
					return &fresh, nil
				}
				// The PATCH is addressed by the stale ID, so stub it rather than
				// let the fake's identifier lookup fail on a synthetic ID.
				fakeVault.PatchTaskStub = func(
					context.Context,
					string,
					map[string]any,
				) (*vaultapi.Task, error) {
					return &vaultapi.Task{ID: "stale-id"}, nil
				}
				// Same for the PUT: the filesystem stub resolves the id on disk, so
				// a synthetic id would fail the lookup. Capture the argument instead.
				var putID string
				fakeVault.PutBodyStub = func(_ context.Context, id string, _ string) error {
					putID = id
					return nil
				}

				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.CompleteCommand{
						TaskIdentifier: lib.TaskIdentifier("idmix-uuid"),
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.PutBodyCallCount()).To(Equal(1))
				Expect(putID).To(Equal("fresh-id"))
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
				// Closing is two typed writes now — PATCH the fields, PUT the
				// Resolution body — so the "wrote exactly once" check becomes
				// "wrote each half once".
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(1))
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
