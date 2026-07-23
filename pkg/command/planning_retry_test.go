// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"fmt"
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
)

var _ = Describe("PlanningRetryGate", func() {
	var (
		ctx           context.Context
		fakeGit       *mocks.GitClient
		fakeCommenter *mocks.PRCommenter
		clock         libtime.CurrentDateTimeGetter
		gate          command.PlanningRetryGate
		taskDir       string
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeGit = &mocks.GitClient{}
		fakeCommenter = &mocks.PRCommenter{}
		clockVal := &libtimemocks.CurrentDateTimeGetter{}
		clockVal.NowReturns(libtime.DateTime(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)))
		clock = clockVal
		taskDir = "tasks"
		fakeGit.PathReturns("/repo")
		gate = command.NewPlanningRetryGate(fakeGit, taskDir, clock, fakeCommenter, metrics.New())
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

	// onDiskFile creates a task file content. When retryCount < 0, the
	// planning_retry_count key is omitted entirely (absent frontmatter state).
	// When withPRMetadata is true, repository and pull_request_number are included.
	onDiskFile := func(taskID string, retryCount int, body string, withPRMetadata ...bool) []byte {
		fm := map[string]interface{}{
			"task_identifier": taskID,
			"task_type":       "pr-review",
			"assignee":        "pr-reviewer-agent",
			"status":          "in_progress",
			"phase":           "planning",
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

	Describe("passthrough cases", func() {
		Context("non-planning phase", func() {
			It("returns handled=false and does not write", func() {
				req := buildPRReviewTask(
					"pr-123",
					"execution",
					"## Result\nStatus: failed\nMessage: boom\n",
				)
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(
					onDiskFile("pr-123", 0, "## Objective\nreview the PR\n"),
					nil,
				)

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
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
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
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
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(
					onDiskFile("pr-123", 0, "## Objective\nreview the PR\n"),
					nil,
				)

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeFalse())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
				Expect(
					testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					) - before,
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
				diskContent := onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				resultBytes, err := capturedModify(diskContent)
				Expect(err).To(BeNil())

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
				diskContent := onDiskFile(
					"pr-123",
					1,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 1/3: first failure at 2026-07-01T11:00:00Z\n",
				)
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes, err := capturedModify(diskContent)
				Expect(err).To(BeNil())

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
				diskContent := onDiskFile(
					"pr-123",
					2,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 2/3: boom at 2026-07-01T11:00:00Z\n",
				)
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes, _ := capturedModify(diskContent)
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
					diskContent := onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true)
					fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
					fakeGit.ReadFileReturns(diskContent, nil)

					var capturedModify func([]byte) ([]byte, error)
					fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
						capturedModify = modify
						if _, invokeErr := modify(diskContent); invokeErr != nil {
							return invokeErr
						}
						return nil
					}

					beforeRetry := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("retry"),
					)
					beforeExhausted := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					)
					handled, err := gate.Handle(ctx, req)
					Expect(err).To(BeNil())
					Expect(handled).To(BeTrue())
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

					resultBytes, err := capturedModify(diskContent)
					Expect(err).To(BeNil())

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
					diskContent := onDiskFile("pr-123", 5, "## Objective\n\nreview the PR\n", true)
					fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
					fakeGit.ReadFileReturns(diskContent, nil)

					var capturedModify func([]byte) ([]byte, error)
					fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
						capturedModify = modify
						if _, invokeErr := modify(diskContent); invokeErr != nil {
							return invokeErr
						}
						return nil
					}

					beforeExhausted := testutil.ToFloat64(
						metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
					)
					handled, err := gate.Handle(ctx, req)
					Expect(err).To(BeNil())
					Expect(handled).To(BeTrue())

					resultBytes, err := capturedModify(diskContent)
					Expect(err).To(BeNil())

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
				diskContent := onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n", true)
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				fakeCommenter.PostCommentReturns(fmt.Errorf("github 503"))

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				beforeExhausted := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes, err := capturedModify(diskContent)
				Expect(err).To(BeNil())

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
				diskContent := onDiskFile("pr-123", 3, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				fakeCommenter.PostCommentReturns(
					fmt.Errorf(
						"planning-retry: cannot resolve PR from task: no pr_url or repository/pull_request_number in frontmatter",
					),
				)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				beforeExhausted := testutil.ToFloat64(
					metrics.PlanningRetryTotal.WithLabelValues("exhausted"),
				)
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes, err := capturedModify(diskContent)
				Expect(err).To(BeNil())

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
				diskContent := onDiskFile("pr-123", -2, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				resultBytes, _ := capturedModify(diskContent)
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
				// FindTaskFilePath reads count=0 from disk
				diskContentCount0 := onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContentCount0, nil)

				// But the modify closure's re-read sees count=1 (simulating a concurrent bump)
				diskContentCount1 := onDiskFile(
					"pr-123",
					1,
					"## Objective\n\nreview the PR\n\n## Progress\n\n- retry 1/3: boom at 2026-07-01T11:00:00Z\n",
				)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContentCount1); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				before := testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))
				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				// Feed the modify closure the count=1 content (simulating the re-read)
				resultBytes, err := capturedModify(diskContentCount1)
				Expect(err).To(BeNil())
				// Should return unchanged
				Expect(string(resultBytes)).To(Equal(string(diskContentCount1)))

				// No metric increment since bump was false
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
				diskContent := onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				_, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())

				resultBytes, _ := capturedModify(diskContent)
				body := string(resultBytes)

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
				diskContent := onDiskFile("pr-123", -1, "## Objective\n\nreview the PR\n")
				fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
				fakeGit.ReadFileReturns(diskContent, nil)

				var capturedModify func([]byte) ([]byte, error)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
					capturedModify = modify
					if _, invokeErr := modify(diskContent); invokeErr != nil {
						return invokeErr
					}
					return nil
				}

				_, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())

				resultBytes, _ := capturedModify(diskContent)
				body := string(resultBytes)

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
})
