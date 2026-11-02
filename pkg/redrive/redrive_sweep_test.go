// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redrive_test

import (
	"context"
	"errors"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/redrive"
)

var _ = Describe("RedriveSweep", func() {
	var (
		ctx       = context.Background()
		gitClient *mocks.GitClient
		publisher *mocks.TaskPublisher
		clock     time.Time
		sweep     redrive.RedriveSweep
		taskDir   = "24 Tasks"
		branch    = base.Branch("prod")
		interval  = time.Hour
	)

	// fixedClock returns a libtime getter pinned to the current clock value.
	fixedClock := func() libtime.CurrentDateTimeGetter {
		return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(clock)
		})
	}

	// taskContent builds a task markdown file with the given frontmatter.
	taskContent := func(id, status, phase, stage, assignee string, extra map[string]string) []byte {
		fm := map[string]interface{}{
			"task_identifier": id,
			"status":          status,
			"phase":           phase,
			"stage":           stage,
		}
		if assignee != "" {
			fm["assignee"] = assignee
		}
		for k, v := range extra {
			fm[k] = v
		}
		y, err := yaml.Marshal(fm)
		Expect(err).NotTo(HaveOccurred())
		return []byte("---\n" + string(y) + "---\nbody\n")
	}

	BeforeEach(func() {
		gitClient = &mocks.GitClient{}
		publisher = &mocks.TaskPublisher{}
		clock = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		sweep = redrive.NewRedriveSweep(
			gitClient, publisher, taskDir, branch, interval, fixedClock(), metrics.New(),
		)
	})

	Describe("SweepOnce", func() {
		Context("with an eligible never-spawned task", func() {
			BeforeEach(func() {
				gitClient.PullReturns(nil)
				gitClient.ListFilesReturns([]string{"24 Tasks/prod-1.md"}, nil)
				gitClient.ReadFileReturns(
					taskContent(
						"11111111-1111-1111-1111-111111111111",
						"in_progress",
						"execution",
						"prod",
						"sentry-analyzer-agent",
						nil,
					),
					nil,
				)
			})

			It("publishes a TaskUpdated event", func() {
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				Expect(publisher.PublishChangedCallCount()).To(Equal(1))
			})

			It("re-publishes only once within the backoff window", func() {
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				clock = clock.Add(30 * time.Minute)
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				Expect(publisher.PublishChangedCallCount()).To(Equal(1))
			})

			It("re-publishes again once the backoff window has elapsed", func() {
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				clock = clock.Add(2 * time.Hour)
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				Expect(publisher.PublishChangedCallCount()).To(Equal(2))
			})
		})

		Context("with ineligible tasks", func() {
			DescribeTable(
				"skips the task",
				func(status, phase, stage, assignee string, extra map[string]string) {
					gitClient.PullReturns(nil)
					gitClient.ListFilesReturns([]string{"24 Tasks/t.md"}, nil)
					gitClient.ReadFileReturnsOnCall(
						0,
						taskContent(
							"11111111-1111-1111-1111-111111111111",
							status,
							phase,
							stage,
							assignee,
							extra,
						),
						nil,
					)
					Expect(sweep.SweepOnce(ctx)).To(Succeed())
					Expect(publisher.PublishChangedCallCount()).To(Equal(0))
				},
				Entry(
					"status not in_progress",
					"next",
					"execution",
					"prod",
					"sentry-analyzer-agent",
					nil,
				),
				Entry(
					"phase not in trigger set",
					"in_progress",
					"todo",
					"prod",
					"sentry-analyzer-agent",
					nil,
				),
				Entry("empty assignee", "in_progress", "execution", "prod", "", nil),
				Entry(
					"stage mismatch",
					"in_progress",
					"execution",
					"dev",
					"sentry-analyzer-agent",
					nil,
				),
				Entry(
					"already spawned (current_job set)",
					"in_progress",
					"execution",
					"prod",
					"sentry-analyzer-agent",
					map[string]string{"current_job": "job-1"},
				),
			)
		})

		Context("when git pull fails", func() {
			It("returns the error and publishes nothing", func() {
				gitClient.PullReturns(errors.New("pull failed"))
				Expect(
					sweep.SweepOnce(ctx),
				).To(MatchError(ContainSubstring("redrive sweep git pull")))
				Expect(publisher.PublishChangedCallCount()).To(Equal(0))
			})
		})

		Context("when a publish fails", func() {
			It("logs and continues with the next task", func() {
				gitClient.PullReturns(nil)
				gitClient.ListFilesReturns([]string{"24 Tasks/a.md", "24 Tasks/b.md"}, nil)
				gitClient.ReadFileReturnsOnCall(
					0,
					taskContent(
						"11111111-1111-1111-1111-111111111111",
						"in_progress",
						"execution",
						"prod",
						"sentry-analyzer-agent",
						nil,
					),
					nil,
				)
				gitClient.ReadFileReturnsOnCall(
					1,
					taskContent(
						"22222222-2222-2222-2222-222222222222",
						"in_progress",
						"execution",
						"prod",
						"sentry-analyzer-agent",
						nil,
					),
					nil,
				)
				publisher.PublishChangedReturnsOnCall(0, errors.New("kafka down"))
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				Expect(publisher.PublishChangedCallCount()).To(Equal(2))
			})
		})

		Context("with files that are not task files", func() {
			It("skips them without publishing", func() {
				gitClient.PullReturns(nil)
				gitClient.ListFilesReturns([]string{"24 Tasks/readme.md"}, nil)
				gitClient.ReadFileReturnsOnCall(0, []byte("no frontmatter here\n"), nil)
				Expect(sweep.SweepOnce(ctx)).To(Succeed())
				Expect(publisher.PublishChangedCallCount()).To(Equal(0))
			})
		})
	})

	Describe("published event", func() {
		It("carries the task identifier", func() {
			gitClient.PullReturns(nil)
			gitClient.ListFilesReturns([]string{"24 Tasks/prod-1.md"}, nil)
			gitClient.ReadFileReturnsOnCall(
				0,
				taskContent(
					"11111111-1111-1111-1111-111111111111",
					"in_progress",
					"execution",
					"prod",
					"sentry-analyzer-agent",
					nil,
				),
				nil,
			)
			Expect(sweep.SweepOnce(ctx)).To(Succeed())
			_, task := publisher.PublishChangedArgsForCall(0)
			Expect(
				task.TaskIdentifier,
			).To(Equal(lib.TaskIdentifier("11111111-1111-1111-1111-111111111111")))
		})
	})
})
