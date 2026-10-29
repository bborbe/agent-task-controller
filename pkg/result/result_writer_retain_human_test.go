// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// These specs drive the WRITER, not the chokepoint function: the chokepoint's own
// table (result_writer_human_assignees_test.go) cannot catch a wiring regression —
// a NewResultWriter that forgot to store the set, or a cap path that still calls
// clearAssignee unconditionally — because it never goes through either.
var _ = Describe("ResultWriter human-owner retention", func() {
	const (
		taskDir    = "tasks"
		identifier = lib.TaskIdentifier("test-task-uuid-1234")
	)

	var (
		ctx      context.Context
		tmpDir   string
		fakeGit  *mocks.GitClient
		taskPath string
	)

	writeTaskFile := func(frontmatter string) {
		content := "---\n" + frontmatter + "\n---\n\nbody\n"
		Expect(
			os.WriteFile(taskPath, []byte(content), 0600),
		).To(Succeed())
		// #nosec G306 -- test helper
	}

	readTaskFile := func() string {
		content, err := os.ReadFile(taskPath) // #nosec G304 -- test helper
		Expect(err).NotTo(HaveOccurred())
		return string(content)
	}

	newWriter := func(humans result.HumanAssignees) result.ResultWriter {
		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)))
		fakeMetrics := &mocks.Metrics{}
		fakeMetrics.ResultsWrittenTotalReturns(prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_results_written_total",
		}))
		return result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			fakeMetrics,
			&libtimemocks.WaiterDuration{},
			nil,
			nil,
			humans,
		)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-retain-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())
		taskPath = filepath.Join(tmpDir, taskDir, "task.md")

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			matches, globErr := filepath.Glob(filepath.Join(tmpDir, glob))
			if globErr != nil {
				return nil, globErr
			}
			var rel []string
			for _, m := range matches {
				r, _ := filepath.Rel(tmpDir, m)
				rel = append(rel, r)
			}
			return rel, nil
		}
		fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			return os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test-only path
		}
		fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
			_ context.Context,
			absPath string,
			modify func([]byte) ([]byte, error),
			_ string,
		) error {
			current, readErr := os.ReadFile(absPath) // #nosec G304 -- test helper
			if readErr != nil {
				return readErr
			}
			updated, modErr := modify(current)
			if modErr != nil {
				return modErr
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	humanReviewResult := func() lib.Task {
		return lib.Task{
			TaskIdentifier: identifier,
			Frontmatter: lib.TaskFrontmatter{
				"status":      "in_progress",
				"phase":       "human_review",
				"assignee":    "bborbe",
				"retry_count": 0,
			},
		}
	}

	It("retains a configured human owner through the writer", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: bborbe")

		Expect(
			newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, humanReviewResult()),
		).
			To(Succeed())

		Expect(readTaskFile()).To(ContainSubstring("assignee: bborbe"))
	})

	It("still clears an agent assignee through the writer", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: pr-reviewer-agent")

		Expect(
			newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, humanReviewResult()),
		).
			To(Succeed())

		written := readTaskFile()
		Expect(written).To(ContainSubstring("previous_assignee: pr-reviewer-agent"))
		// Assert the exact cleared line, not a substring: "previous_assignee:
		// pr-reviewer-agent" itself contains "assignee: pr-reviewer-agent".
		Expect(written).To(ContainSubstring(`assignee: ""`))
	})

	It("retains a human owner when the retry cap fires in the same write", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: bborbe" +
			"\nmax_retries: 1\nretry_count: 1")

		retryCapped := humanReviewResult()
		retryCapped.Frontmatter["max_retries"] = 1
		retryCapped.Frontmatter["retry_count"] = 1

		Expect(newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, retryCapped)).
			To(Succeed())

		written := readTaskFile()
		Expect(written).To(ContainSubstring("assignee: bborbe"))
		Expect(strings.Count(written, "## Retry Escalation")).To(Equal(1))
	})

	It("clears an agent assignee when the retry cap fires", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: pr-reviewer-agent" +
			"\nmax_retries: 1\nretry_count: 1")

		retryCapped := humanReviewResult()
		retryCapped.Frontmatter["assignee"] = "pr-reviewer-agent"
		retryCapped.Frontmatter["max_retries"] = 1
		retryCapped.Frontmatter["retry_count"] = 1

		Expect(newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, retryCapped)).
			To(Succeed())

		written := readTaskFile()
		Expect(written).To(ContainSubstring(`assignee: ""`))
		Expect(written).To(ContainSubstring("## Retry Escalation"))
	})

	// The trigger cap is the sibling path: applyTriggerCap runs BEFORE the
	// human_review chokepoint inside one write, so it needs its own retention case.
	// Without one, routing it through clearAssigneeUnlessHuman is unproven and a
	// regression there would silently strip the owner on every capped record.
	It("retains a human owner when the trigger cap fires in the same write", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: bborbe" +
			"\nmax_triggers: 1\ntrigger_count: 1")

		triggerCapped := humanReviewResult()
		triggerCapped.Frontmatter["max_triggers"] = 1
		triggerCapped.Frontmatter["trigger_count"] = 1

		Expect(newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, triggerCapped)).
			To(Succeed())

		written := readTaskFile()
		Expect(written).To(ContainSubstring("assignee: bborbe"))
		Expect(strings.Count(written, "## Trigger Cap Escalation")).To(Equal(1))
	})

	It("clears an agent assignee when the trigger cap fires", func() {
		writeTaskFile("task_identifier: " + string(identifier) +
			"\nstatus: in_progress\nphase: human_review\nassignee: pr-reviewer-agent" +
			"\nmax_triggers: 1\ntrigger_count: 1")

		triggerCapped := humanReviewResult()
		triggerCapped.Frontmatter["assignee"] = "pr-reviewer-agent"
		triggerCapped.Frontmatter["max_triggers"] = 1
		triggerCapped.Frontmatter["trigger_count"] = 1

		Expect(newWriter(result.ParseHumanAssignees("bborbe")).WriteResult(ctx, triggerCapped)).
			To(Succeed())

		written := readTaskFile()
		Expect(written).To(ContainSubstring("previous_assignee: pr-reviewer-agent"))
		Expect(written).To(ContainSubstring(`assignee: ""`))
		Expect(written).To(ContainSubstring("## Trigger Cap Escalation"))
	})
})
