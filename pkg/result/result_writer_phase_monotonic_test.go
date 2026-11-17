// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/testvault"
)

// The fall-through behind the planning-retry gate: a result whose payload still
// carries a stale `phase: planning` used to land on a task parked in `execution`
// or `human_review`, because `phase` is agent-owned in the merge. The write then
// re-arms the task and the executor spawns a second run with no second claim.
var _ = Describe("ResultWriter stale payload phase", func() {
	const parkedInExecution = `---
status: in_progress
phase: execution
assignee: pr-reviewer-agent
task_type: pr-review
task_identifier: stale-phase-uuid
target_vault: openclaw
---
# Objective

review the PR
`
	var (
		ctx        context.Context
		tmpDir     string
		fakeGit    *testvault.Client
		writer     result.ResultWriter
		identifier lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-stale-phase-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())

		fakeGit = &testvault.Client{Dir: tmpDir}

		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)))

		identifier = lib.TaskIdentifier("stale-phase-uuid")
		writer = result.NewResultWriter(
			fakeGit,
			"tasks",
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
		)
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "Stale phase.md"),
			[]byte(parkedInExecution),
			0600,
		)).To(Succeed())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	It("keeps the on-disk execution phase when the payload echoes planning", func() {
		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"status":       "in_progress",
				"phase":        "planning",
				"assignee":     "pr-reviewer-agent",
				"task_type":    "pr-review",
				"target_vault": "openclaw",
			},
			Content: lib.TaskContent("## Result\nStatus: failed\nMessage: boom\n"),
		})).To(Succeed())

		raw, readErr := os.ReadFile(filepath.Join(tmpDir, "tasks", "Stale phase.md"))
		Expect(readErr).NotTo(HaveOccurred())
		GinkgoWriter.Printf("resulting on-disk file:\n%s\n", raw)

		task, err := fakeGit.ReadTask(ctx, string(identifier))
		Expect(err).NotTo(HaveOccurred())
		Expect(task.Frontmatter["phase"]).To(Equal("execution"))
		// The guard pins the phase only: the failure record still lands.
		Expect(task.Body).To(ContainSubstring("Status: failed"))
	})

	It("still advances the on-disk phase when the payload carries a later one", func() {
		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"status":       "in_progress",
				"phase":        "human_review",
				"assignee":     "pr-reviewer-agent",
				"task_type":    "pr-review",
				"target_vault": "openclaw",
			},
			Content: lib.TaskContent("## Result\nStatus: done\n"),
		})).To(Succeed())

		task, err := fakeGit.ReadTask(ctx, string(identifier))
		Expect(err).NotTo(HaveOccurred())
		Expect(task.Frontmatter["phase"]).To(Equal("human_review"))
	})
})

var _ = Describe("MergeFrontmatter phase monotonicity", func() {
	It("keeps the on-disk phase and reports a decision when the payload is behind", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "phase": "execution"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": "planning"}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("execution"))
		Expect(decisions).To(HaveLen(1))
		Expect(decisions[0].Field).To(Equal("phase"))
		Expect(decisions[0].Kept).To(Equal("execution"))
		Expect(decisions[0].Rejected).To(Equal("planning"))
	})

	It("keeps the on-disk phase when the payload echoes planning over human_review", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "phase": "human_review"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": "planning"}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("human_review"))
		Expect(decisions).To(HaveLen(1))
	})

	It("applies a payload phase that advances the on-disk phase", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "phase": "planning"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": "execution"}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("execution"))
		Expect(decisions).To(BeEmpty())
	})

	It("applies a payload phase when the task carries none on disk", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": "planning"}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("planning"))
		Expect(decisions).To(BeEmpty())
	})

	It("does not erase the on-disk phase when the payload carries an empty phase", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "phase": "execution"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": ""}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("execution"))
		Expect(decisions).To(HaveLen(1))
	})

	It("applies an unknown payload phase rather than guessing its position", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "phase": "execution"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "phase": "brand-new-phase"}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("brand-new-phase"))
		Expect(decisions).To(BeEmpty())
	})

	// The sentry deep-to-fix handoff is a deliberate result-payload write that
	// re-arms the phase backwards (execution -> planning) and declares it. The
	// monotonic pin must not discard it.
	It("applies a backwards payload phase when the payload declares a reassignment", func() {
		existing := lib.TaskFrontmatter{
			"assignee":  "sentry-analyzer-agent",
			"task_type": "sentry-deep-analyzer",
			"phase":     "execution",
			"status":    "in_progress",
		}
		incoming := lib.TaskFrontmatter{
			"assignee":               "sentry-fix-agent",
			"assignee_reassigned_by": "sentry-deep-execution-fix-handoff",
			"task_type":              "sentry-fix",
			"phase":                  "planning",
			"status":                 "in_progress",
		}

		merged, decisions := result.MergeFrontmatter(existing, incoming)

		Expect(merged["phase"]).To(Equal("planning"))
		Expect(decisions).To(BeEmpty())
	})
})
