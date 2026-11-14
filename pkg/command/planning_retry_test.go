// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/routing"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("PlanningRetryGate", func() {
	var (
		ctx           context.Context
		tmpDir        string
		fakeVault     *mocks.VaultAPIClient
		fakeCommenter *mocks.PRCommenter
		clock         libtime.CurrentDateTimeGetter
		gate          command.PlanningRetryGate
		taskDir       string
		// seed writes a task file the FS-backed client addresses by identifier.
		seed func(id string, content []byte)
		// stored reads the task file back, which is what the old suite took from
		// the git fake's captured write closure.
		stored func(id string) []byte
		// moveAfterFirstRead replaces the on-disk file once the gate has taken its
		// first read, simulating a task that moves between the two reads.
		moveAfterFirstRead func(id string, content []byte)
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "planning-retry")
		Expect(err).To(BeNil())
		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0700)).To(BeNil())

		seed = func(id string, content []byte) {
			Expect(
				os.WriteFile(filepath.Join(tmpDir, taskDir, id+".md"), content, 0600),
			).To(BeNil())
		}
		stored = func(id string) []byte {
			raw, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, id+".md"))
			Expect(readErr).To(BeNil())
			return raw
		}

		fakeVault = newFSTaskClient(tmpDir, taskDir)
		baseRead := fakeVault.ReadTaskStub
		reads := 0
		var movedID string
		var moved []byte
		moveAfterFirstRead = func(id string, content []byte) {
			movedID = id
			moved = content
		}
		fakeVault.ReadTaskStub = func(
			readCtx context.Context,
			id string,
		) (*vaultapi.Task, error) {
			reads++
			if reads > 1 && moved != nil && id == movedID {
				seed(id, moved)
				moved = nil
			}
			return baseRead(readCtx, id)
		}

		fakeCommenter = &mocks.PRCommenter{}
		clockVal := &libtimemocks.CurrentDateTimeGetter{}
		clockVal.NowReturns(libtime.DateTime(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)))
		clock = clockVal
		gate = command.NewPlanningRetryGate(
			fakeVault,
			taskDir,
			"openclaw",
			clock,
			fakeCommenter,
			metrics.New(),
			nil,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(BeNil())
	})

	buildPRReviewTask := func(taskID string, phaseVal string, content string) lib.Task {
		now := libtime.DateTime(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
		return lib.Task{
			Object: base.Object[base.Identifier]{
				Identifier: base.Identifier(taskID),
				Created:    now,
				Modified:   now,
			},
			TaskIdentifier: lib.TaskIdentifier(taskID),
			Frontmatter: lib.TaskFrontmatter{
				"task_type": "pr-review",
				"phase":     phaseVal,
				"assignee":  "pr-reviewer-agent",
			},
			Content: lib.TaskContent(content),
		}
	}

	// onDiskFileWithPhase creates a task file content with an explicit on-disk phase.
	onDiskFileWithPhase := func(taskID string, phase string, retryCount int, body string, withPRMetadata ...bool) []byte {
		fm := map[string]interface{}{
			"task_identifier": taskID,
			"task_type":       "pr-review",
			"assignee":        "pr-reviewer-agent",
			"status":          "in_progress",
			"phase":           phase,
		}
		if retryCount >= 0 {
			fm["planning_retry_count"] = retryCount
		}
		if len(withPRMetadata) > 0 && withPRMetadata[0] {
			fm["repository"] = "bborbe/maintainer"
			fm["pull_request_number"] = 62
		}
		fmBytes, _ := yaml.Marshal(fm)
		return []byte("---\n" + string(fmBytes) + "\n---\n" + body)
	}

	// onDiskFile creates a task file content. When retryCount < 0, the
	// planning_retry_count key is omitted entirely (absent frontmatter state).
	// When withPRMetadata is true, repository and pull_request_number are included.
	onDiskFile := func(taskID string, retryCount int, body string, withPRMetadata ...bool) []byte {
		return onDiskFileWithPhase(taskID, "planning", retryCount, body, withPRMetadata...)
	}

	Describe("passthrough cases", func() {
		Context("non-planning phase", func() {
			It("returns handled=false and does not write", func() {
				req := buildPRReviewTask(
					"pr-123",
					"execution",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed(
					"pr-123",
					onDiskFileWithPhase("pr-123", "execution", 0, "## Objective\nreview the PR\n"),
				)

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(0.0))
			})
		})

		Context("non-pr-review task type", func() {
			It("returns handled=false", func() {
				req := lib.Task{
					TaskIdentifier: lib.TaskIdentifier("task-llm"),
					Frontmatter: lib.TaskFrontmatter{
						"task_type": "llm",
						"phase":     "planning",
						"assignee":  "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\nMessage: boom\n"),
				}

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(0.0))
			})
		})

		Context("success result", func() {
			It("returns handled=false and does not write", func() {
				req := buildPRReviewTask("pr-123", "planning", "## Result\nStatus: done\n")
				seed("pr-123", onDiskFile("pr-123", 0, "## Objective\nreview the PR\n"))

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(0.0))
			})
		})
	})

	Describe("on-disk phase is the source of truth", func() {
		Context("payload carries no phase, on-disk phase is planning", func() {
			It("retries based on the on-disk phase", func() {
				req := lib.Task{
					TaskIdentifier: lib.TaskIdentifier("pr-123"),
					Frontmatter: lib.TaskFrontmatter{
						"task_type": "pr-review",
						"assignee":  "pr-reviewer-agent",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\nMessage: boom\n"),
				}
				Expect(req.Frontmatter.Phase()).To(BeNil())

				seed("pr-123", onDiskFile("pr-123", 0, "## Objective\n\nreview the PR\n"))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(1))

				resultFM, err := result.ExtractFrontmatter(ctx, stored("pr-123"))
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())
				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(1))
			})
		})

		Context("payload phase is planning, on-disk phase is execution", func() {
			It("skips without writing", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				Expect(req.Frontmatter.Phase().String()).To(Equal("planning"))

				diskContent := onDiskFileWithPhase(
					"pr-123",
					"execution",
					0,
					"## Objective\n\nreview the PR\n",
				)
				diskFMStr, diskErr := result.ExtractFrontmatter(ctx, diskContent)
				Expect(diskErr).To(BeNil())
				var diskFM lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(diskFMStr), &diskFM)).To(BeNil())
				Expect(diskFM.Phase().String()).To(Equal("execution"))

				seed("pr-123", diskContent)

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
			})
		})

		// The mirror of the context above: a payload that claims a phase PAST
		// planning must not veto a retry while the task is still parked in
		// planning. Without this spec, a reintroduced payload-side veto
		// ("payload phase present and != planning => skip") would pass every
		// other spec in this block, and "the payload phase is never consulted"
		// would be locked in one direction only.
		Context("payload phase is execution, on-disk phase is planning", func() {
			It("retries on the on-disk phase and ignores the payload phase", func() {
				req := buildPRReviewTask(
					"pr-123",
					"execution",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				Expect(req.Frontmatter.Phase().String()).To(Equal("execution"))

				diskContent := onDiskFileWithPhase(
					"pr-123",
					"planning",
					0,
					"## Objective\n\nreview the PR\n",
				)
				diskFMStr, diskErr := result.ExtractFrontmatter(ctx, diskContent)
				Expect(diskErr).To(BeNil())
				var diskFM lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(diskFMStr), &diskFM)).To(BeNil())
				Expect(diskFM.Phase().String()).To(Equal("planning"))

				seed("pr-123", diskContent)

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(1))
			})
		})

		Context("on-disk phase is absent", func() {
			It("skips without writing", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", []byte(
					"---\ntask_identifier: pr-123\ntask_type: pr-review\nassignee: pr-reviewer-agent\nstatus: in_progress\nplanning_retry_count: 0\n---\n## Objective\n\nreview the PR\n",
				))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
			})
		})

		Context("task file not found", func() {
			It("skips before consulting the phase", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))
			})
		})
	})

	Describe("the on-disk phase is re-checked on the second read", func() {
		// Handle reads the task, then re-reads before writing. A task that moves
		// past planning between those two reads must not be forced back — neither
		// by the retry write nor by the escalation write. The read hook replaces
		// the file after the first read, which is exactly what a concurrent writer
		// does.
		Context("retry path: the task moves past planning before the write", func() {
			It("leaves the file untouched and does not bump the counter", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 0, "## Objective\n\nreview the PR\n"))
				moved := onDiskFileWithPhase(
					"pr-123",
					"execution",
					0,
					"## Objective\n\nreview the PR\n",
				)
				moveAfterFirstRead("pr-123", moved)

				beforeRetry := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("retry"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(stored("pr-123")).To(Equal(moved))
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))

				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - beforeRetry,
				).To(Equal(0.0))
			})
		})

		Context("escalation path: the task moves past planning before the write", func() {
			It("leaves the file untouched and posts no escalation comment", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true))
				moved := onDiskFileWithPhase(
					"pr-123",
					"human_review",
					3,
					"## Objective\n\nreview the PR\n",
					true,
				)
				moveAfterFirstRead("pr-123", moved)

				beforeExhausted := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(stored("pr-123")).To(Equal(moved))
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))

				Expect(fakeCommenter.PostCommentCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					) - beforeExhausted,
				).To(Equal(0.0))
			})
		})
	})

	Describe("retry attempts", func() {
		Context("retry attempt 1 (count absent -> 0)", func() {
			It("bumps counter to 1 and sets fresh task_identifier", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: minimax B-case empty plan\n",
				)
				// On-disk file has no planning_retry_count key
				seed("pr-123", onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n"))

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(1))

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(1))

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("planning"))

				newTaskID, _ := fm.String("task_identifier")
				parsedUUID, err := uuid.Parse(newTaskID)
				Expect(err).To(BeNil())
				Expect(parsedUUID).NotTo(BeNil())
				Expect(newTaskID).NotTo(Equal("pr-123"))

				body := string(resultBytes)
				Expect(body).To(ContainSubstring("retry 1/3:"))
				retryLineRE := regexp.MustCompile(`(?m)^- retry 1/3: .* at 2026-07-01T12:00:00Z`)
				Expect(retryLineRE.MatchString(body)).To(BeTrue())

				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(1.0))
			})
		})

		Context("retry attempt 2 (count 1 -> 2)", func() {
			It("bumps counter to 2 and preserves existing retry line", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile(
					"pr-123",
					1,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 1/3: first failure at 2026-07-01T11:00:00Z\n",
				))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, _ := result.ExtractFrontmatter(ctx, resultBytes)
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())
				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(2))

				body := string(resultBytes)
				Expect(body).To(ContainSubstring("retry 1/3:"))
				Expect(body).To(ContainSubstring("retry 2/3:"))
			})
		})

		Context("retry attempt 3 (count 2 -> 3)", func() {
			It("bumps counter to 3", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile(
					"pr-123",
					2,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 2/3: boom at 2026-07-01T11:00:00Z\n",
				))

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, _ := result.ExtractFrontmatter(ctx, resultBytes)
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())
				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(3))

				body := string(resultBytes)
				Expect(body).To(ContainSubstring("retry 3/3:"))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(1.0))
			})
		})
	})

	Describe("cap and defensive cases", func() {
		Context("counter at cap (3) -> escalates to human_review", func() {
			It(
				"returns handled=true, phase=human_review, assignee cleared, COMMENT posted, exhausted metric incremented",
				func() {
					req := buildPRReviewTask(
						"pr-123",
						"planning",
						"## Result\nStatus: failed\nMessage: boom\n",
					)
					seed("pr-123", onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true))

					beforeRetry := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					)
					beforeExhausted := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					)
					handled, err := gate.Handle(ctx, req)
					Expect(err).To(BeNil())
					Expect(handled).To(BeTrue())
					Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(1))

					resultBytes := stored("pr-123")
					resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
					Expect(err).To(BeNil())
					var fm lib.TaskFrontmatter
					Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

					phase, _ := fm.String("phase")
					Expect(phase).To(Equal("human_review"))

					assignee, _ := fm.String("assignee")
					Expect(assignee).To(Equal(""))

					body := string(resultBytes)
					Expect(body).To(ContainSubstring("retry 3/3:"))

					Expect(
						testutil.ToFloat64(
							metrics.PlanningRetryTotal.WithLabelValues("retry"),
						) - beforeRetry,
					).To(Equal(0.0))
					Expect(
						testutil.ToFloat64(
							metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
						) - beforeExhausted,
					).To(Equal(1.0))

					Expect(fakeCommenter.PostCommentCallCount()).To(Equal(1))
					_, commentFM, commentBody := fakeCommenter.PostCommentArgsForCall(0)
					Expect(commentFM["repository"]).To(Equal("bborbe/maintainer"))
					Expect(commentFM["pull_request_number"]).To(Equal(62))
					Expect(
						commentBody,
					).To(ContainSubstring("Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error:"))
					Expect(commentBody).To(ContainSubstring("Please investigate tasks/pr-123.md."))
				},
			)
		})

		Context("defensive counter > 3 -> treated as at-cap, same escalation", func() {
			It(
				"returns handled=true, escalation fires, COMMENT posted, counter not normalized",
				func() {
					req := buildPRReviewTask(
						"pr-123",
						"planning",
						"## Result\nStatus: failed\nMessage: boom\n",
					)
					seed("pr-123", onDiskFile("pr-123", 5, "## Objective\n\nreview the PR\n", true))

					beforeExhausted := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					)
					handled, err := gate.Handle(ctx, req)
					Expect(err).To(BeNil())
					Expect(handled).To(BeTrue())

					resultBytes := stored("pr-123")
					resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
					Expect(err).To(BeNil())
					var fm lib.TaskFrontmatter
					Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

					phase, _ := fm.String("phase")
					Expect(phase).To(Equal("human_review"))

					assignee, _ := fm.String("assignee")
					Expect(assignee).To(Equal(""))

					Expect(
						testutil.ToFloat64(
							metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
						) - beforeExhausted,
					).To(Equal(1.0))

					Expect(fakeCommenter.PostCommentCallCount()).To(Equal(1))

					count, _ := fm.Int("planning_retry_count")
					Expect(count).To(Equal(5))
				},
			)
		})

		Context("GitHub error swallowed - COMMENT fails but escalation still lands", func() {
			It("returns handled=true, phase=human_review, exhausted metric incremented", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true))

				fakeCommenter.PostCommentReturns(fmt.Errorf("github 503"))

				beforeExhausted := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("human_review"))

				assignee, _ := fm.String("assignee")
				Expect(assignee).To(Equal(""))

				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					) - beforeExhausted,
				).To(Equal(1.0))

				Expect(fakeCommenter.PostCommentCallCount()).To(Equal(1))
			})
		})

		Context("missing PR metadata - COMMENT skipped but escalation still lands", func() {
			It("returns handled=true, phase=human_review, exhausted metric incremented", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n"))

				fakeCommenter.PostCommentReturns(
					fmt.Errorf(
						"planning-retry: cannot resolve PR from task: no pr_url or repository/pull_request_number in frontmatter",
					),
				)

				beforeExhausted := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("human_review"))

				assignee, _ := fm.String("assignee")
				Expect(assignee).To(Equal(""))

				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					) - beforeExhausted,
				).To(Equal(1.0))

				Expect(fakeCommenter.PostCommentCallCount()).To(Equal(1))
			})
		})

		Context("defensive negative counter -> clamped to 0, retry attempt 1", func() {
			It("bumps counter to 1", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", -2, "## Objective\n\nreview the PR\n"))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, _ := result.ExtractFrontmatter(ctx, resultBytes)
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())
				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(1))
			})
		})
	})

	Describe("idempotency", func() {
		Context("redelivery -> concurrent bump detected, no-op", func() {
			It("returns handled=true but does not increment counter", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				// The first read sees count absent -> 0
				seed("pr-123", onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n"))

				// The second read sees count=1 (simulating a concurrent bump)
				diskContentCount1 := onDiskFile(
					"pr-123",
					1,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 1/3: boom at 2026-07-01T11:00:00Z\n",
				)
				moveAfterFirstRead("pr-123", diskContentCount1)

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				// The second read saw the bumped counter, so the write never ran.
				Expect(stored("pr-123")).To(Equal(diskContentCount1))
				Expect(fakeVault.PatchTaskWithBodyCallCount()).To(Equal(0))

				// No metric increment since no bump happened
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
				).To(Equal(0.0))
			})
		})
	})

	Describe("reason sanitization", func() {
		Context("reason with newlines and CR stripped", func() {
			It("produces retry line without literal newlines in reason", func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: line one\rline two\nmore\n",
				)
				seed("pr-123", onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n"))

				_, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())

				body := string(stored("pr-123"))

				for _, line := range strings.Split(body, "\n") {
					if strings.Contains(line, "retry 1/3:") {
						Expect(strings.Contains(line, "\n")).To(BeFalse())
						Expect(strings.Contains(line, "\r")).To(BeFalse())
					}
				}
			})
		})

		Context("reason truncated to 200 runes", func() {
			It("produces reason portion <= 200 runes", func() {
				longReason := strings.Repeat("x", 300)
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: "+longReason+"\n",
				)
				seed("pr-123", onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n"))

				_, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())

				body := string(stored("pr-123"))

				for _, line := range strings.Split(body, "\n") {
					if strings.Contains(line, "retry 1/3:") {
						// Extract reason portion: "retry 1/3: <reason> at <ts>"
						prefix := "retry 1/3: "
						idx := strings.Index(line, prefix)
						if idx >= 0 {
							reasonPart := line[idx+len(prefix):]
							atIdx := strings.Index(reasonPart, " at ")
							if atIdx >= 0 {
								reasonStr := reasonPart[:atIdx]
								Expect(
									utf8.RuneCountInString(reasonStr),
								).To(BeNumerically("<=", 200))
							}
						}
					}
				}
			})
		})
	})

	Describe("heal-on-write (target_vault stamping)", func() {
		It(
			"stamps target_vault on the retry write for a file lacking it",
			func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 0, "## Objective\n\nreview the PR\n"))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				target, _ := fm.String("target_vault")
				Expect(target).To(Equal("openclaw"))

				// the stamped file read back through ShouldProcessResult no longer
				// falls through to the non-owning controller
				stampedReq := lib.Task{
					TaskIdentifier: lib.TaskIdentifier("pr-123"),
					Frontmatter:    lib.TaskFrontmatter{"target_vault": "openclaw"},
				}
				Expect(routing.ShouldProcessResult(stampedReq, "personal")).To(BeFalse())
				Expect(routing.ShouldProcessResult(stampedReq, "openclaw")).To(BeTrue())
			},
		)

		It(
			"stamps target_vault on the escalation write for a file lacking it",
			func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				seed("pr-123", onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				target, _ := fm.String("target_vault")
				Expect(target).To(Equal("openclaw"))

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("human_review"))

				stampedReq := lib.Task{
					TaskIdentifier: lib.TaskIdentifier("pr-123"),
					Frontmatter:    lib.TaskFrontmatter{"target_vault": "openclaw"},
				}
				Expect(routing.ShouldProcessResult(stampedReq, "personal")).To(BeFalse())
				Expect(routing.ShouldProcessResult(stampedReq, "openclaw")).To(BeTrue())
			},
		)

		It(
			"never overrides an existing target_vault on retry or escalation writes",
			func() {
				req := buildPRReviewTask(
					"pr-123",
					"planning",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				diskContent := onDiskFile("pr-123", 0, "## Objective\n\nreview the PR\n")
				// rewrite disk content with a pre-existing target_vault
				stamped := strings.Replace(
					string(diskContent),
					"status: in_progress",
					"status: in_progress\ntarget_vault: personal",
					1,
				)
				seed("pr-123", []byte(stamped))

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes := stored("pr-123")
				resultFM, err := result.ExtractFrontmatter(ctx, resultBytes)
				Expect(err).To(BeNil())
				var fm lib.TaskFrontmatter
				Expect(yaml.Unmarshal([]byte(resultFM), &fm)).To(BeNil())

				target, _ := fm.String("target_vault")
				Expect(target).To(Equal("personal"))
			},
		)
	})
})
