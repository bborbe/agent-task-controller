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

// Body-section merge specs live in their own file (result_writer_test.go is at
// the 2000-line revive file-length-limit) but nest under the same
// Describe("ResultWriter")/Describe("WriteResult") tree so the full spec names
// match where the Context would have sat between the field-ownership guard and
// the interleaved-partial-update contexts. The harness is duplicated here
// because Ginkgo BeforeEach does not cross top-level Describe blocks.
var _ = Describe("ResultWriter", func() {
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
		tmpDir, err = os.MkdirTemp("", "result-writer-body-merge-*")
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
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
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

	Describe("WriteResult", func() {
		Context("field ownership guard", func() {
			DescribeTable(
				"the on-disk operator-owned field always wins over a stale incoming snapshot",
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
					"keeps the on-disk empty assignee (operator park) over a stale incoming name",
					"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: \"\"\nprevious_assignee: github-update-go-agent\n",
					lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"assignee":        "github-update-go-agent",
					},
					[]string{"assignee: \"\"", "previous_assignee: github-update-go-agent"},
					// line-anchored: the bare "assignee: github-update-go-agent" substring
					// collides with previous_assignee, so anchor to the frontmatter line
					[]string{"\nassignee: github-update-go-agent"},
				),
				Entry(
					"keeps the on-disk previous_assignee over a stale incoming value",
					"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nprevious_assignee: A\n",
					lib.TaskFrontmatter{
						"task_identifier":   "test-task-uuid-1234",
						"status":            "in_progress",
						"phase":             "ai_review",
						"previous_assignee": "B",
					},
					[]string{"previous_assignee: A"},
					[]string{"previous_assignee: B"},
				),
			)

			It(
				"applies an incoming empty assignee even when the on-disk assignee is non-empty (deliverer clear)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\n## Result\nStatus: failed\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"assignee":        "",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("assignee: \"\""))
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				},
			)

			It(
				"introduces an incoming assignee when the on-disk frontmatter has no assignee key (spawn/claim)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nStatus: failed\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"assignee":        "backtest-agent",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("assignee: backtest-agent"))
				},
			)
		})

		Context("body section merge", func() {
			It(
				"preserves an on-disk-only heading and its content when the incoming body lacks it (operator ## Parked)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Parked\n\nOperator park reason, resume options.\n\n## Result\nStatus: failed\n",
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
					Expect(s).To(ContainSubstring("## Parked"))
					Expect(s).To(ContainSubstring("Operator park reason, resume options."))
					Expect(s).To(ContainSubstring("## Result"))
					Expect(s).To(ContainSubstring("Status: failed"))
				},
			)

			It(
				"replaces a same-named heading in place with the incoming content (fresh ## Result lands)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nOld content\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
						},
						Content: lib.TaskContent("## Result\nNew content\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("New content"))
					Expect(s).NotTo(ContainSubstring("Old content"))
				},
			)

			It(
				"preserves the on-disk preamble when the incoming body starts with a heading",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\nTags: [[Task]]\n\n---\n\ndescription\n\n## Details\n- x\n",
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
					Expect(s).To(ContainSubstring("Tags: [[Task]]"))
					Expect(s).To(ContainSubstring("description"))
					Expect(s).To(ContainSubstring("## Result"))
				},
			)

			It(
				"replaces a preamble-only on-disk body with an incoming preamble-only body (no headings)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\nFirst content\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
						},
						Content: lib.TaskContent("Second result\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("Second result\n"))
					Expect(s).NotTo(ContainSubstring("First content"))
				},
			)

			It(
				"tolerates CRLF line endings in the on-disk body and preserves an on-disk-only section",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Parked\r\n\r\nOperator park reason.\r\n\r\n## Result\r\nStatus: failed\r\n",
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
					Expect(s).To(ContainSubstring("## Parked"))
					Expect(s).To(ContainSubstring("Operator park reason."))
					Expect(s).To(ContainSubstring("## Result"))
				},
			)
		})
	})
})
