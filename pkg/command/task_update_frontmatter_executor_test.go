// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/agent/lib"
	task "github.com/bborbe/agent/lib/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("NewUpdateFrontmatterExecutor", func() {
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
		tmpDir, err = os.MkdirTemp("", "update-fm-test-*")
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
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}

		executor = command.NewUpdateFrontmatterExecutor(fakeGit, taskDir)
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
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
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
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
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
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
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
	})
})
