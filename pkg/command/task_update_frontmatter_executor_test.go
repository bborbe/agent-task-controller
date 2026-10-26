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
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/routing"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("NewUpdateFrontmatterExecutor", func() {
	var (
		ctx       context.Context
		tmpDir    string
		taskDir   string
		fakeVault *mocks.VaultAPIClient
		executor  cdb.CommandObjectExecutorTx
		schemaID  cdb.SchemaID
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "update-fm-test-*")
		Expect(err).NotTo(HaveOccurred())

		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

		fakeVault = newFSTaskClient(tmpDir, taskDir)
		executor = command.NewUpdateFrontmatterExecutor(
			fakeVault,
			"openclaw",
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

	parseFrontmatter := func(absPath string) map[string]interface{} {
		content, err := os.ReadFile(absPath) // #nosec G304 -- test helper
		Expect(err).NotTo(HaveOccurred())
		s := string(content)
		Expect(s).To(HavePrefix("---\n"))
		rest := s[4:]
		before, _, found := strings.Cut(rest, "\n---\n")
		Expect(found).To(BeTrue())
		var fm map[string]interface{}
		Expect(yaml.Unmarshal([]byte(before), &fm)).To(Succeed())
		return fm
	}

	buildCmdObj := func(cmd task.UpdateFrontmatterCommand) cdb.CommandObject {
		event, err := base.ParseEvent(ctx, cmd)
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				RequestID: base.NewRequestID(),
				Operation: command.UpdateFrontmatterCommandOperation,
				Initiator: "test",
				Data:      event,
			},
			SchemaID: schemaID,
		}
	}

	Describe("CommandOperation", func() {
		It("returns update-frontmatter", func() {
			Expect(
				executor.CommandOperation(),
			).To(Equal(base.CommandOperation("update-frontmatter")))
		})
	})

	Describe("HandleCommand", func() {
		Context("only named keys change", func() {
			It("updates only the specified key and leaves others unchanged", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: update-test-uuid\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("update-test-uuid"),
					Updates: lib.TaskFrontmatter{
						"phase": "done",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["phase"]).To(Equal("done"))
				Expect(fm["status"]).To(Equal("in_progress"))
				Expect(fm["assignee"]).To(Equal("claude"))
			})
		})

		Context("empty updates", func() {
			It("returns nil without writing when Updates is nil", func() {
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: noop-uuid\nstatus: open\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("noop-uuid"),
					Updates:        nil,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
			})

			It("returns nil without writing when Updates is empty map", func() {
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: noop2-uuid\nstatus: open\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("noop2-uuid"),
					Updates:        lib.TaskFrontmatter{},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
			})
		})

		Context("task not found", func() {
			It("returns nil without writing when no matching file exists", func() {
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("nonexistent-uuid"),
						Updates:        lib.TaskFrontmatter{"phase": "human_review"},
					}),
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
			})
		})

		Context("multiple updates", func() {
			It("applies all specified keys without touching unspecified ones", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: multi-update-uuid\nstatus: in_progress\nphase: ai_review\nassignee: claude\ncustom: preserve\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("multi-update-uuid"),
					Updates: lib.TaskFrontmatter{
						"status": "completed",
						"phase":  "done",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["status"]).To(Equal("completed"))
				Expect(fm["phase"]).To(Equal("done"))
				Expect(fm["assignee"]).To(Equal("claude"))
				Expect(fm["custom"]).To(Equal("preserve"))
			})
		})

		Context("PATCH payload", func() {
			It("PATCHes only the fields the command names, not the whole frontmatter", func() {
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: payload-uuid\nstatus: in_progress\nphase: ai_review\nassignee: claude\ncustom: preserve\ntarget_vault: openclaw\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("payload-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())

				// The central claim of the typed port: the request carries the
				// changed field and nothing else. A whole-frontmatter PATCH would
				// land the same final file through the fake and pass unnoticed.
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))
				_, id, fields := fakeVault.PatchTaskArgsForCall(0)
				Expect(id).To(Equal("payload-uuid"))
				Expect(fields).To(Equal(map[string]any{"phase": "done"}))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
			})

			It("sends no PATCH when the command restates the current value", func() {
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: noop3-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("noop3-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "ai_review"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
			})

			It("does not panic on a task carrying a list-valued frontmatter field", func() {
				// Regression: frontmatter arrives JSON-decoded, so `tags:` is a
				// []any and comparing two of them with `!=` panics with
				// "comparing uncomparable type []interface {}" — for EVERY update
				// command against such a task, not just ones naming the list.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: list-uuid\nstatus: in_progress\nphase: ai_review\ntags:\n  - alpha\n  - beta\ntarget_vault: openclaw\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("list-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))
				_, _, fields := fakeVault.PatchTaskArgsForCall(0)
				Expect(fields).To(Equal(map[string]any{"phase": "done"}))

				fm := parseFrontmatter(taskFile)
				Expect(fm["tags"]).To(ConsistOf("alpha", "beta"))
				Expect(fm["phase"]).To(Equal("done"))
			})

			It("returns an error when PutBody fails after the PATCH landed", func() {
				// The documented partial-application window: the two writes are
				// not atomic, so a body failure leaves frontmatter updated. The
				// command is redelivered and re-runs both halves.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: partial-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n",
				)
				fakeVault.PutBodyReturns(errors.New("body write failed"))
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("partial-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "human_review"},
					Body: &task.BodySection{
						Heading: "## Failure",
						Section: "## Failure\n\n- **Reason:** OOMKilled\n",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).To(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))

				fm := parseFrontmatter(taskFile)
				Expect(fm["phase"]).To(Equal("human_review"))
				content, readErr := os.ReadFile(taskFile) // #nosec G304 -- test helper
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(content)).NotTo(ContainSubstring("## Failure"))
			})
		})

		Context("body merge re-reads the task", func() {
			It("keeps a body section that landed between the read and the PUT", func() {
				// PUT replaces the WHOLE body, so merging onto the body captured at
				// the top of the command would silently drop an edit made in
				// between. The first read is stubbed to return the older body;
				// the re-read before the PUT must see the operator's section.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: reread-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n\n## Operator\n\nkeep me\n",
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

				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("reread-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
					Body: &task.BodySection{
						Heading: "## Result",
						Section: "## Result\n\nfinished\n",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.ReadTaskCallCount()).To(Equal(2))
				content, readErr := os.ReadFile(taskFile) // #nosec G304 -- test helper
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(content)).To(ContainSubstring("## Operator"))
				Expect(string(content)).To(ContainSubstring("keep me"))
				Expect(string(content)).To(ContainSubstring("## Result"))
			})
		})

		Context("failure modes", func() {
			It("surfaces a read failure at the top of the executor", func() {
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: readfail-uuid\nstatus: in_progress\n---\nbody\n",
				)
				before := testutil.ToFloat64(
					metrics.FrontmatterCommandsTotal.WithLabelValues("update-frontmatter", "error"),
				)
				fakeVault.ReadTaskReturns(nil, errors.New("vault-api unreachable"))
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("readfail-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).To(HaveOccurred())
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.FrontmatterCommandsTotal.WithLabelValues(
							"update-frontmatter",
							"error",
						),
					),
				).To(Equal(before + 1))
			})

			It("surfaces a re-read failure that happens after the PATCH landed", func() {
				// The top-of-executor read must succeed, so only the SECOND read is
				// stubbed to fail — the one applyUpdate makes before its PUT. This
				// is the branch the re-read added; stubbing every read would fail
				// the first one and never reach it.
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: rereadfail-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n",
				)
				before := testutil.ToFloat64(
					metrics.FrontmatterCommandsTotal.WithLabelValues("update-frontmatter", "error"),
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
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("rereadfail-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "human_review"},
					Body: &task.BodySection{
						Heading: "## Failure",
						Section: "## Failure\n\n- **Reason:** OOMKilled\n",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).To(HaveOccurred())
				Expect(
					testutil.ToFloat64(
						metrics.FrontmatterCommandsTotal.WithLabelValues(
							"update-frontmatter",
							"error",
						),
					),
				).To(Equal(before + 1))
				// the PATCH landed before the re-read failed; the body write did not
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))
				Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
				Expect(parseFrontmatter(taskFile)["phase"]).To(Equal("human_review"))
			})

			It("addresses the body PUT by the re-read task's id, not the stale one", func() {
				// The filesystem fake gives both reads the same ID, so without a
				// stub that makes them differ, reverting fresh.ID to task.ID leaves
				// the suite green — the hardening would be untested.
				writeTaskFile(
					"task.md",
					"---\ntask_identifier: idmix-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n",
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
				// Both writes are addressed by a synthetic id, so stub them rather
				// than let the fake's identifier lookup fail on it.
				fakeVault.PatchTaskStub = func(
					context.Context,
					string,
					map[string]any,
				) (*vaultapi.Task, error) {
					return &vaultapi.Task{ID: "stale-id"}, nil
				}
				var putID string
				fakeVault.PutBodyStub = func(_ context.Context, id string, _ string) error {
					putID = id
					return nil
				}

				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("idmix-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
					Body: &task.BodySection{
						Heading: "## Result",
						Section: "## Result\n\nfinished\n",
					},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeVault.PutBodyCallCount()).To(Equal(1))
				Expect(putID).To(Equal("fresh-id"))
			})

			It("surfaces a PATCH failure and counts it as an error", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: patchfail-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: openclaw\n---\nbody\n",
				)
				before := testutil.ToFloat64(
					metrics.FrontmatterCommandsTotal.WithLabelValues("update-frontmatter", "error"),
				)
				fakeVault.PatchTaskReturns(nil, errors.New("patch rejected"))
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("patchfail-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).To(HaveOccurred())
				Expect(
					testutil.ToFloat64(
						metrics.FrontmatterCommandsTotal.WithLabelValues(
							"update-frontmatter",
							"error",
						),
					),
				).To(Equal(before + 1))
				// nothing was written: the PATCH was the only write and it failed
				fm := parseFrontmatter(taskFile)
				Expect(fm["phase"]).To(Equal("ai_review"))
			})
		})

		Context("Body field appends a new section", func() {
			It(
				"updates frontmatter and appends the ## Failure section while preserving ## Result",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: body-append-uuid\nstatus: in_progress\nphase: ai_review\n---\n## Result\n\nok\n",
					)
					failureSection := "## Failure\n\n- **Timestamp:** 2026-04-24T12:00:00Z\n- **Job:** job-abc\n- **Reason:** OOMKilled\n"
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("body-append-uuid"),
						Updates: lib.TaskFrontmatter{
							"status":      "in_progress",
							"phase":       "human_review",
							"current_job": "",
						},
						Body: &task.BodySection{
							Heading: "## Failure",
							Section: failureSection,
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(err).NotTo(HaveOccurred())

					fm := parseFrontmatter(taskFile)
					Expect(fm["phase"]).To(Equal("human_review"))

					content, err := os.ReadFile(taskFile) // #nosec G304 -- test helper
					Expect(err).NotTo(HaveOccurred())
					body := string(content)
					Expect(body).To(ContainSubstring("## Result"))
					Expect(body).To(ContainSubstring("## Failure"))
					Expect(body).To(ContainSubstring("OOMKilled"))
				},
			)
		})

		Context("Body nil leaves body untouched", func() {
			It("updates frontmatter without modifying the existing body", func() {
				originalBody := "## Result\n\nok\n"
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: body-nil-uuid\nstatus: in_progress\nphase: ai_review\n---\n"+originalBody,
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("body-nil-uuid"),
					Updates: lib.TaskFrontmatter{
						"phase": "human_review",
					},
					Body: nil,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())

				fm := parseFrontmatter(taskFile)
				Expect(fm["phase"]).To(Equal("human_review"))

				content, err := os.ReadFile(taskFile) // #nosec G304 -- test helper
				Expect(err).NotTo(HaveOccurred())
				// Body should be unchanged
				Expect(string(content)).To(ContainSubstring(originalBody))
				Expect(string(content)).NotTo(ContainSubstring("## Failure"))
			})
		})

		Context("spec 042: phase flip to human_review clears assignee", func() {
			It(
				"sets previous_assignee and clears assignee when Updates flips phase to human_review",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: spec-042-flip-uuid\nstatus: in_progress\nphase: planning\nassignee: pr-reviewer-agent\ncurrent_job: pr-reviewer-agent-e323cc47\n---\nbody\n",
					)
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("spec-042-flip-uuid"),
						Updates: lib.TaskFrontmatter{
							"phase": "human_review",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(err).NotTo(HaveOccurred())
					fm := parseFrontmatter(taskFile)
					Expect(fm["phase"]).To(Equal("human_review"))
					Expect(fm["assignee"]).To(Equal(""))
					Expect(fm["previous_assignee"]).To(Equal("pr-reviewer-agent"))
					Expect(fm["current_job"]).To(Equal("pr-reviewer-agent-e323cc47"))
					Expect(fm["status"]).To(Equal("in_progress"))
				},
			)
		})

		Context("spec 042: non-phase updates leave assignee untouched", func() {
			It(
				"does not clear assignee and does not add previous_assignee when Updates contains no phase key",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: spec-042-nonphase-uuid\nstatus: in_progress\nphase: in_progress\nassignee: backtest-agent\n---\nbody\n",
					)
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("spec-042-nonphase-uuid"),
						Updates: lib.TaskFrontmatter{
							"progress": "50%",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(err).NotTo(HaveOccurred())
					fm := parseFrontmatter(taskFile)
					Expect(fm["assignee"]).To(Equal("backtest-agent"))
					Expect(fm["phase"]).To(Equal("in_progress"))
					Expect(fm["progress"]).To(Equal("50%"))
					_, hasPrev := fm["previous_assignee"]
					Expect(hasPrev).To(BeFalse(), "non-phase update must not add previous_assignee")
				},
			)
		})

		Context("spec 042: idempotent re-clear on already-parked task", func() {
			It(
				"preserves previous_assignee on a parked task when Updates is a non-phase key with Body",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: spec-042-parked-uuid\nstatus: in_progress\nphase: human_review\nassignee: \"\"\nprevious_assignee: pr-reviewer-agent\n---\n## Result\n\nok\n",
					)
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("spec-042-parked-uuid"),
						Updates: lib.TaskFrontmatter{
							"verdict": "fail",
						},
						Body: &task.BodySection{
							Heading: "## Verdict",
							Section: "## Verdict\n\n- **Result:** fail\n",
						},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(err).NotTo(HaveOccurred())
					fm := parseFrontmatter(taskFile)
					Expect(fm["phase"]).To(Equal("human_review"))
					Expect(fm["assignee"]).To(Equal(""))
					// previous_assignee must NOT be overwritten with empty string —
					// clearAssignee only writes previous_assignee when current assignee is non-empty.
					Expect(fm["previous_assignee"]).To(Equal("pr-reviewer-agent"))
					Expect(fm["verdict"]).To(Equal("fail"))
					content, err := os.ReadFile(taskFile) // #nosec G304 -- test helper
					Expect(err).NotTo(HaveOccurred())
					Expect(string(content)).To(ContainSubstring("## Verdict"))
					Expect(string(content)).To(ContainSubstring("## Result"))
				},
			)
		})

		Context(
			"spec 042: prod incident reproducer (phase: human_review + Body Verdict section)",
			func() {
				It(
					"clears assignee, captures previous_assignee, and appends the ## Verdict body section in a single atomic write",
					func() {
						taskFile := writeTaskFile(
							"task.md",
							"---\ntask_identifier: spec-042-incident-uuid\nstatus: in_progress\nphase: planning\nassignee: pr-reviewer-agent\ncurrent_job: pr-reviewer-agent-e323cc47\n---\nbody\n",
						)
						verdictSection := "## Verdict\n\n- **Verdict:** fail\n- **Reason:** hallucination detected in PR diff\n"
						cmd := buildCmdObj(task.UpdateFrontmatterCommand{
							TaskIdentifier: lib.TaskIdentifier("spec-042-incident-uuid"),
							Updates: lib.TaskFrontmatter{
								"phase": "human_review",
							},
							Body: &task.BodySection{
								Heading: "## Verdict",
								Section: verdictSection,
							},
						})
						_, _, err := executor.HandleCommand(ctx, nil, cmd)
						Expect(err).NotTo(HaveOccurred())

						fm := parseFrontmatter(taskFile)
						Expect(fm["phase"]).To(Equal("human_review"))
						Expect(fm["assignee"]).To(Equal(""))
						Expect(fm["previous_assignee"]).To(Equal("pr-reviewer-agent"))

						content, err := os.ReadFile(taskFile) // #nosec G304 -- test helper
						Expect(err).NotTo(HaveOccurred())
						body := string(content)
						Expect(body).To(ContainSubstring("## Verdict"))
						Expect(body).To(ContainSubstring("hallucination detected"))
					},
				)
			},
		)

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
							"update-frontmatter",
							"not_found",
						),
					)
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("cross-vault-uuid"),
						TargetVault:    "personal",
						Updates:        lib.TaskFrontmatter{"phase": "done"},
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd)
					Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
					// guard fires before the task read and before any write
					Expect(fakeVault.ReadTaskCallCount()).To(Equal(0))
					Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))
					Expect(fakeVault.PutBodyCallCount()).To(Equal(0))
					Expect(
						testutil.ToFloat64(
							metrics.FrontmatterCommandsTotal.WithLabelValues(
								"update-frontmatter",
								"not_found",
							),
						),
					).To(Equal(before))
					// file untouched
					fm := parseFrontmatter(taskFile)
					Expect(fm["phase"]).To(Equal("ai_review"))
					_, hasTarget := fm["target_vault"]
					Expect(hasTarget).To(BeFalse())
				},
			)

			It("processes an empty-vault command (legacy fall-through)", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: legacy-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("legacy-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				// The write is a typed PATCH now, so "wrote exactly once" moves to
				// that seam. The file content check is what proves it landed.
				Expect(fakeVault.PatchTaskCallCount()).To(Equal(1))
				fm := parseFrontmatter(taskFile)
				Expect(fm["phase"]).To(Equal("done"))
			})

			It(
				"heals a file lacking target_vault, stamping the controller vault in the same write",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: heal-uuid\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
					)
					cmd := buildCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("heal-uuid"),
						Updates:        lib.TaskFrontmatter{"phase": "done"},
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
					// and the frontmatter-command predicate rejects the non-owner too,
					// so subsequent update/increment commands for this file skip cleanly
					Expect(
						routing.ShouldProcessFrontmatterCommand("openclaw", "personal"),
					).To(BeFalse())
					Expect(
						routing.ShouldProcessFrontmatterCommand("openclaw", "openclaw"),
					).To(BeTrue())
				},
			)

			It("never overrides an existing target_vault", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: stamped-uuid\nstatus: in_progress\nphase: ai_review\ntarget_vault: personal\n---\nbody\n",
				)
				cmd := buildCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("stamped-uuid"),
					Updates:        lib.TaskFrontmatter{"phase": "done"},
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["target_vault"]).To(Equal("personal"))
				Expect(fm["phase"]).To(Equal("done"))
			})
		})
	})
})
