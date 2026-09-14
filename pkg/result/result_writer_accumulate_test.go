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

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// The two cumulative counters (metrics_agent_turns, metrics_interaction_count) are
// summed by the result write-back merge when both the task file and the agent's
// result payload carry a numeric value (spec 012). These specs assert on the bytes
// the write-back leaves on disk, which is the only place the accumulated total is
// observable: accumulation is a transform, not a guard discard, so it produces no
// decision and no log line to assert on. Split into its own file because
// result_writer_test.go is at the repo's 2000-line file-length-limit gate.
var _ = Describe("ResultWriter accumulated counters", func() {
	var (
		ctx        context.Context
		tmpDir     string
		taskDir    string
		fakeGit    *mocks.GitClient
		fakeTime   *libtimemocks.CurrentDateTimeGetter
		writer     result.ResultWriter
		taskFile   lib.Task
		identifier lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-accumulate-*")
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
			return os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test-only path
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

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)))

		identifier = lib.TaskIdentifier("test-task-uuid-1234")
		writer = result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	writeTaskFile := func(name, content string) string {
		absPath := filepath.Join(tmpDir, taskDir, name)
		Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())
		return absPath
	}

	DescribeTable(
		"the accumulated counters add the incoming value to the on-disk total",
		func(onDiskFM string, incomingFM lib.TaskFrontmatter, present, absent []string) {
			writeTaskFile("my-task.md", "---\n"+onDiskFM+"---\n## Result\nStatus: failed\n")
			taskFile = lib.Task{
				TaskIdentifier: identifier,
				Frontmatter:    incomingFM,
				Content:        lib.TaskContent("## Result\nStatus: failed\n"),
			}
			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
			written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			s := string(written)
			for _, want := range present {
				Expect(s).To(ContainSubstring(want))
			}
			for _, unwanted := range absent {
				Expect(s).NotTo(ContainSubstring(unwanted))
			}
		},
		Entry(
			"accumulates metrics_agent_turns (7 on disk + 3 incoming = 10)",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 7\n",
			lib.TaskFrontmatter{
				"task_identifier":     "test-task-uuid-1234",
				"status":              "in_progress",
				"phase":               "ai_review",
				"metrics_agent_turns": float64(3),
			},
			[]string{"metrics_agent_turns: 10"},
			[]string{"metrics_agent_turns: 3", "metrics_agent_turns: 7"},
		),
		Entry(
			"accumulates metrics_interaction_count (116 on disk + 41 incoming = 157)",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_interaction_count: 116\n",
			lib.TaskFrontmatter{
				"task_identifier":           "test-task-uuid-1234",
				"status":                    "in_progress",
				"phase":                     "ai_review",
				"metrics_interaction_count": float64(41),
			},
			[]string{"metrics_interaction_count: 157"},
			[]string{"metrics_interaction_count: 41", "metrics_interaction_count: 116"},
		),
		Entry(
			"an evidenced unattended run (incoming 0) does not wipe the total",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_interaction_count: 116\n",
			lib.TaskFrontmatter{
				"task_identifier":           "test-task-uuid-1234",
				"status":                    "in_progress",
				"phase":                     "ai_review",
				"metrics_interaction_count": float64(0),
			},
			[]string{"metrics_interaction_count: 116"},
			[]string{"metrics_interaction_count: 0"},
		),
		Entry(
			"the first run on a task that never carried the counters writes the emitted values",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n",
			lib.TaskFrontmatter{
				"task_identifier":           "test-task-uuid-1234",
				"status":                    "in_progress",
				"phase":                     "ai_review",
				"metrics_agent_turns":       float64(3),
				"metrics_interaction_count": float64(0),
			},
			[]string{"metrics_agent_turns: 3", "metrics_interaction_count: 0"},
			[]string{},
		),
		Entry(
			"keeps a non-numeric on-disk counter verbatim over a numeric incoming value",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: unparseable\n",
			lib.TaskFrontmatter{
				"task_identifier":     "test-task-uuid-1234",
				"status":              "in_progress",
				"phase":               "ai_review",
				"metrics_agent_turns": float64(3),
			},
			[]string{"metrics_agent_turns: unparseable"},
			[]string{"metrics_agent_turns: 3"},
		),
		Entry(
			"keeps the on-disk counter when the incoming value is non-numeric",
			"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 3\n",
			lib.TaskFrontmatter{
				"task_identifier":     "test-task-uuid-1234",
				"status":              "in_progress",
				"phase":               "ai_review",
				"metrics_agent_turns": "unparseable",
			},
			[]string{"metrics_agent_turns: 3"},
			[]string{"metrics_agent_turns: unparseable"},
		),
	)

	It(
		"leaves both on-disk counters verbatim when the incoming payload carries neither key",
		func() {
			writeTaskFile(
				"my-task.md",
				"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 7\nmetrics_interaction_count: 116\n---\n## Result\nStatus: failed\n",
			)
			taskFile = lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier": "test-task-uuid-1234",
					"status":          "in_progress",
					"phase":           "ai_review",
				},
				Content: lib.TaskContent("## Result\nStatus: failed\n"),
			}
			Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
			written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			s := string(written)
			Expect(s).To(ContainSubstring("metrics_agent_turns: 7"))
			Expect(s).To(ContainSubstring("metrics_interaction_count: 116"))
		},
	)

	Context("accumulation across runs (spec 012)", func() {
		It("accumulates the second run's emission onto the first run's total", func() {
			writeTaskFile(
				"my-task.md",
				"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nStatus: failed\n",
			)

			// First run: the payload emits 3 turns onto a task that never carried the key.
			Expect(writer.WriteResult(ctx, lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier":     "test-task-uuid-1234",
					"status":              "in_progress",
					"phase":               "ai_review",
					"metrics_agent_turns": float64(3),
				},
				Content: lib.TaskContent("## Result\nfirst run\n"),
			})).To(Succeed())

			afterFirst, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(afterFirst)).To(ContainSubstring("metrics_agent_turns: 3"))

			// Second run: the payload emits 4 turns; the write-back reads the file the
			// first call wrote and accumulates onto it.
			Expect(writer.WriteResult(ctx, lib.Task{
				TaskIdentifier: identifier,
				Frontmatter: lib.TaskFrontmatter{
					"task_identifier":     "test-task-uuid-1234",
					"status":              "in_progress",
					"phase":               "ai_review",
					"metrics_agent_turns": float64(4),
				},
				Content: lib.TaskContent("## Result\nsecond run\n"),
			})).To(Succeed())

			afterSecond, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(afterSecond)).To(ContainSubstring("metrics_agent_turns: 7"))
			Expect(string(afterSecond)).NotTo(ContainSubstring("metrics_agent_turns: 4"))
			Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
		})

		It(
			"accumulates the counter on a terminal task while the on-disk status stays pinned",
			func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: completed\nphase: done\nmetrics_agent_turns: 7\n---\n## Result\nStatus: failed\n",
				)
				Expect(writer.WriteResult(ctx, lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier":     "test-task-uuid-1234",
						"status":              "in_progress",
						"phase":               "ai_review",
						"metrics_agent_turns": float64(3),
					},
					Content: lib.TaskContent("## Result\nlate result\n"),
				})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(err).NotTo(HaveOccurred())
				s := string(written)
				Expect(s).To(ContainSubstring("metrics_agent_turns: 10"))
				Expect(s).NotTo(ContainSubstring("metrics_agent_turns: 7"))
				Expect(s).To(ContainSubstring("status: completed"))
				Expect(s).NotTo(ContainSubstring("status: in_progress"))
			},
		)
	})
})
