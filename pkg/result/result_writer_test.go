// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/agent/lib"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

var errTest = errors.New("test error")

func extractTestFrontmatter(content string) (map[string]interface{}, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, errors.New("no frontmatter")
	}
	rest := content[4:]
	before, _, found := strings.Cut(rest, "\n---\n")
	if !found {
		return nil, errors.New("frontmatter not closed")
	}
	var fm map[string]interface{}
	if err := yaml.Unmarshal([]byte(before), &fm); err != nil {
		return nil, err
	}
	return fm, nil
}

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
		tmpDir, err = os.MkdirTemp("", "result-writer-test-*")
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
		writer = result.NewResultWriter(fakeGit, taskDir, fakeTime)
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
		Context("normal write", func() {
			It("writes frontmatter and content to the matched file", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in-progress\nassignee: backtest-agent\ntags:\n  - agent-task\n  - test\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "done",
						"phase":           "done",
					},
					Content: lib.TaskContent("New content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(written)).To(HavePrefix("---\n"))
				Expect(string(written)).To(ContainSubstring("status: done"))
				Expect(string(written)).To(ContainSubstring("phase: done"))
				Expect(string(written)).To(ContainSubstring("assignee: backtest-agent"))
				Expect(string(written)).To(ContainSubstring("agent-task"))
				Expect(string(written)).To(ContainSubstring("---\nNew content\n"))

				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				_, _, _, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(msg).To(ContainSubstring(string(identifier)))
			})
		})

		Context("frontmatter merge", func() {
			It("preserves existing frontmatter keys not sent by agent", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nassignee: backtest-agent\ntags:\n  - a\n  - b\ncustom_field: hello\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "completed",
						"phase":           "done",
					},
					Content: lib.TaskContent("New content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				s := string(written)
				Expect(s).To(ContainSubstring("assignee: backtest-agent"))
				Expect(s).To(ContainSubstring("custom_field: hello"))
				Expect(s).To(ContainSubstring("status: completed"))
				Expect(s).To(ContainSubstring("phase: done"))
				Expect(s).To(ContainSubstring("task_identifier: test-task-uuid-1234"))
				// tags preserved
				Expect(s).To(ContainSubstring("- a"))
				Expect(s).To(ContainSubstring("- b"))
			})

			It("agent keys override existing keys", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: in_progress\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "completed",
						"phase":           "done",
					},
					Content: lib.TaskContent("New content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				s := string(written)
				Expect(s).To(ContainSubstring("status: completed"))
				Expect(s).To(ContainSubstring("phase: done"))
				Expect(s).NotTo(ContainSubstring("in_progress"))
			})
		})

		Context("overwrite", func() {
			It("fully replaces content on second call", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in-progress\n---\nFirst content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "done",
					},
					Content: lib.TaskContent("First result\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

				taskFile.Content = lib.TaskContent("Second result\n")
				taskFile.Frontmatter["status"] = "closed"
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(written)).To(ContainSubstring("Second result\n"))
				Expect(string(written)).To(ContainSubstring("status: closed"))
				Expect(string(written)).NotTo(ContainSubstring("First result"))

				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
			})
		})

		Context("unknown task identifier", func() {
			It("returns nil without committing when no matching file found", func() {
				writeTaskFile(
					"other-task.md",
					"---\ntask_identifier: different-uuid\nstatus: open\n---\nSome content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: lib.TaskIdentifier("non-existent-uuid"),
					Frontmatter:    lib.TaskFrontmatter{"status": "done"},
					Content:        lib.TaskContent("Result\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})

		Context("empty frontmatter", func() {
			It("preserves existing keys when agent sends empty frontmatter", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nassignee: backtest-agent\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter:    lib.TaskFrontmatter{},
					Content:        lib.TaskContent("New content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				s := string(written)
				Expect(s).To(ContainSubstring("task_identifier: test-task-uuid-1234"))
				Expect(s).To(ContainSubstring("assignee: backtest-agent"))
				Expect(s).To(ContainSubstring("---\nNew content\n"))
			})
		})

		Context("frontmatter with nested values", func() {
			It("serializes lists and nested maps correctly", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: open\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "done",
						"tags":            []interface{}{"agent-task", "automated"},
						"meta": map[string]interface{}{
							"model": "claude-sonnet-4-6",
						},
					},
					Content: lib.TaskContent("Result content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())

				// Parse and verify frontmatter
				s := string(written)
				Expect(s).To(HavePrefix("---\n"))
				parts := strings.SplitN(s[4:], "\n---\n", 2)
				Expect(parts).To(HaveLen(2))

				var parsedFm map[string]interface{}
				Expect(yaml.Unmarshal([]byte(parts[0]), &parsedFm)).To(Succeed())
				Expect(parsedFm["status"]).To(Equal("done"))

				tags, ok := parsedFm["tags"].([]interface{})
				Expect(ok).To(BeTrue())
				Expect(tags).To(ContainElements("agent-task", "automated"))

				meta, ok := parsedFm["meta"].(map[string]interface{})
				Expect(ok).To(BeTrue())
				Expect(meta["model"]).To(Equal("claude-sonnet-4-6"))

				Expect(parts[1]).To(Equal("Result content\n"))
			})
		})

		Context("content with YAML delimiters", func() {
			It("preserves bare --- lines in body without escaping", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: open\n---\nOld content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "done",
					},
					Content: lib.TaskContent(
						"## Result\n\nOutput:\n---\nsome yaml block\n---\nDone.\n",
					),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())

				s := string(written)
				// Verify frontmatter is parseable
				Expect(s).To(HavePrefix("---\n"))
				parts := strings.SplitN(s[4:], "\n---\n", 2)
				Expect(parts).To(HaveLen(2))

				var parsedFm map[string]interface{}
				Expect(yaml.Unmarshal([]byte(parts[0]), &parsedFm)).To(Succeed())
				Expect(parsedFm["status"]).To(Equal("done"))

				// Body --- preserved as-is (valid markdown horizontal rule)
				Expect(parts[1]).To(ContainSubstring("\n---\n"))
				Expect(parts[1]).NotTo(ContainSubstring(`\-\-\-`))

				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
			})
		})

		Context("realistic end-to-end", func() {
			It(
				"reads a real Obsidian task, applies agent result, and produces correct output",
				func() {
					// Realistic task file matching actual Obsidian format:
					// frontmatter + Tags line + --- separator + body
					originalTask := `---
task_identifier: e2e-uuid-1234-5678
status: in_progress
phase: ai_review
assignee: backtest-agent
stage: dev
planned_date: "2026-04-17"
page_type: task
tags:
  - agent-task
  - backtest
---
Tags: [[Task]] [[Trading]]

---

Run a backtest for strategy **capitalcom-backtest-BACKTEST** from 2026-04-10 to 2026-04-17.

## Details

- **Strategy:** capitalcom-backtest-BACKTEST
- **From:** 2026-04-10
- **Until:** 2026-04-17
`
					writeTaskFile("e2e-backtest.md", originalTask)

					// Simulate agent result: status completed, body includes --- separators
					taskFile = lib.Task{
						TaskIdentifier: lib.TaskIdentifier("e2e-uuid-1234-5678"),
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "e2e-uuid-1234-5678",
							"status":          "completed",
							"phase":           "done",
						},
						Content: lib.TaskContent(`Tags: [[Task]] [[Trading]]

---

Run a backtest for strategy **capitalcom-backtest-BACKTEST** from 2026-04-10 to 2026-04-17.

## Details

- **Strategy:** capitalcom-backtest-BACKTEST
- **From:** 2026-04-10
- **Until:** 2026-04-17

## Result

- **Strategy:** capitalcom-backtest-BACKTEST
- **Period:** 2026-04-10 — 2026-04-17
- **Backtest ID:** b3b44eb0-60d9-40b9-9e7d-5afdc3272020
- **Status:** DONE
`),
					}

					err := writer.WriteResult(ctx, taskFile)
					Expect(err).NotTo(HaveOccurred())

					written, readErr := os.ReadFile(
						filepath.Join(tmpDir, taskDir, "e2e-backtest.md"),
					)
					Expect(readErr).NotTo(HaveOccurred())
					s := string(written)

					// 1. File starts with frontmatter delimiter
					Expect(s).To(HavePrefix("---\n"))

					// 2. Parse frontmatter correctly
					parts := strings.SplitN(s[4:], "\n---\n", 2)
					Expect(parts).To(HaveLen(2), "frontmatter must be closed by ---")

					var parsedFm map[string]interface{}
					Expect(yaml.Unmarshal([]byte(parts[0]), &parsedFm)).To(Succeed())

					// 3. Agent keys override existing
					Expect(parsedFm["status"]).To(Equal("completed"))
					Expect(parsedFm["phase"]).To(Equal("done"))
					Expect(parsedFm["task_identifier"]).To(Equal("e2e-uuid-1234-5678"))

					// 4. Existing keys NOT in agent result are preserved
					Expect(parsedFm["assignee"]).To(Equal("backtest-agent"))
					Expect(parsedFm["stage"]).To(Equal("dev"))
					Expect(parsedFm["page_type"]).To(Equal("task"))
					Expect(parsedFm["planned_date"]).To(Equal("2026-04-17"))

					// 5. Tags list preserved
					tags, ok := parsedFm["tags"].([]interface{})
					Expect(ok).To(BeTrue(), "tags should be a list")
					Expect(tags).To(ContainElements("agent-task", "backtest"))

					// 6. Body contains --- as-is (not escaped to \-\-\-)
					body := parts[1]
					Expect(body).To(ContainSubstring("\n---\n"), "body --- must be preserved")
					Expect(body).NotTo(ContainSubstring(`\-\-\-`), "body --- must NOT be escaped")

					// 7. Body contains result section
					Expect(body).To(ContainSubstring("## Result"))
					Expect(body).To(ContainSubstring("b3b44eb0-60d9-40b9-9e7d-5afdc3272020"))
					Expect(body).To(ContainSubstring("DONE"))

					// 8. Body contains Tags line (Obsidian links)
					Expect(body).To(ContainSubstring("Tags: [[Task]] [[Trading]]"))

					// 9. Committed exactly once
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

					// 10. Verify the full file can be re-read and re-parsed
					// (simulates controller reading it again on next cycle)
					reParsedFm, reParseErr := extractTestFrontmatter(s)
					Expect(reParseErr).NotTo(HaveOccurred())
					Expect(reParsedFm["status"]).To(Equal("completed"))
					Expect(reParsedFm["phase"]).To(Equal("done"))
				},
			)
		})

		Context("retry counter", func() {
			It("does not modify retry_count on failure and keeps ai_review phase", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nassignee: claude\nretry_count: 1\n---\nAgent output\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
					},
					Content: lib.TaskContent("Result body\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(
					s,
				).To(ContainSubstring("retry_count: 1"))
				// unchanged — executor owns the counter
				Expect(s).To(ContainSubstring("phase: ai_review"))
				Expect(s).NotTo(ContainSubstring("human_review"))
			})

			It(
				"escalates when retry_count (set by executor) meets default max_retries, preserving lifecycle phase",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nretry_count: 3\nassignee: claude\n---\nAgent output\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"retry_count":     3,
						},
						Content: lib.TaskContent("Agent output\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("retry_count: 3")) // unchanged — executor set it
					// phase preserved at lifecycle stage where cap fired (not overwritten to human_review)
					Expect(s).To(ContainSubstring("phase: ai_review"))
					Expect(s).NotTo(ContainSubstring("phase: human_review"))
					Expect(s).To(ContainSubstring("## Retry Escalation"))
					Expect(s).To(ContainSubstring("**Attempts:** 3"))
					Expect(
						s,
					).To(ContainSubstring("**Assignee:** claude"))
					// section text records pre-clear agent
					Expect(
						s,
					).NotTo(ContainSubstring("\nassignee: claude"))
					// frontmatter assignee cleared
					Expect(s).To(ContainSubstring("2026-04-18T12:00:00Z"))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It("does not modify retry_count when result is completed", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nretry_count: 2\nassignee: claude\n---\nAgent output\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "completed",
						"phase":           "done",
					},
					Content: lib.TaskContent("Success output\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("retry_count: 2"))
				Expect(s).To(ContainSubstring("phase: done"))
				Expect(s).NotTo(ContainSubstring("human_review"))
				Expect(s).NotTo(ContainSubstring("Retry Escalation"))
			})

			It(
				"escalates immediately when retry_count (set by executor) meets max_retries 0, preserving lifecycle phase",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nmax_retries: 0\nretry_count: 1\nassignee: claude\n---\nAgent output\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"max_retries":     0,
							"retry_count":     1,
						},
						Content: lib.TaskContent("Agent output\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("retry_count: 1")) // unchanged
					// phase preserved at lifecycle stage where cap fired
					Expect(s).To(ContainSubstring("phase: ai_review"))
					Expect(s).NotTo(ContainSubstring("phase: human_review"))
					Expect(s).To(ContainSubstring("## Retry Escalation"))
					Expect(
						s,
					).NotTo(ContainSubstring("\nassignee: claude"))
					// frontmatter assignee cleared
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It("does not escalate when retry_count (set by executor) is below max_retries", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nretry_count: 3\nmax_retries: 5\nassignee: claude\n---\nAgent output\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"retry_count":     3,
						"max_retries":     5,
					},
					Content: lib.TaskContent("Agent output\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("retry_count: 3")) // unchanged — 3 < 5, no escalation
				Expect(s).NotTo(ContainSubstring("human_review"))
				Expect(s).NotTo(ContainSubstring("Retry Escalation"))
			})

			It("writes assignee: empty and preserves phase: ai_review at retry cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nretry_count: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"retry_count":     3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: ai_review"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Retry Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})

			It("writes assignee: empty and preserves phase: execution at retry cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\nretry_count: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "execution",
						"retry_count":     3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: execution"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Retry Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})

			It("writes assignee: empty and preserves phase: planning at retry cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\nretry_count: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "planning",
						"retry_count":     3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: planning"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Retry Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})
		})

		Context("spawn notification", func() {
			It("does not increment retry_count when spawn_notification is true", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\nretry_count: 0\n---\nOriginal body\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier":    "test-task-uuid-1234",
						"status":             "in_progress",
						"phase":              "ai_review",
						"spawn_notification": true,
						"current_job":        "claude-20260418120000",
						"job_started_at":     "2026-04-18T12:00:00Z",
					},
					Content: lib.TaskContent("Original body\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("retry_count: 0"))
				Expect(s).To(ContainSubstring("current_job: claude-20260418120000"))
				Expect(s).To(ContainSubstring("2026-04-18T12:00:00Z"))
				Expect(s).To(ContainSubstring("phase: ai_review"))
				Expect(s).NotTo(ContainSubstring("human_review"))
				Expect(s).NotTo(ContainSubstring("Retry Escalation"))
				Expect(s).NotTo(ContainSubstring("spawn_notification"))
			})

			It("does not modify retry_count when spawn_notification is absent", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\nretry_count: 0\n---\nOriginal body\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
					},
					Content: lib.TaskContent("Result body\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(
					s,
				).To(ContainSubstring("retry_count: 0"))
				// unchanged — executor owns the counter
			})
		})

		Context(
			"spawn_notification + human_review handoff (spec 041 prod incident reproducer)",
			func() {
				It(
					"clears assignee when merged frontmatter has spawn_notification:true + incoming phase:human_review, spec 041 reproducer",
					func() {
						// Reproducer for the 2026-05-25 prod incident (task bborbe-agent #3).
						// On-disk file: executor's spawn-time UpdateFrontmatterCommand wrote
						// spawn_notification: true onto the file. The agent's first post-spawn
						// result publish carries Result.NextPhase: human_review via resolveNextPhase.
						// mergeFrontmatter inherits spawn_notification: true from disk and merges in
						// phase: human_review from the incoming frontmatter. Before the fix, the
						// spawn_notification early return short-circuited before the human_review
						// guard, leaving assignee: pr-reviewer-agent on disk and missing the
						// operator inbox filter (assignee == "").
						writeTaskFile(
							"my-task.md",
							"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: pr-reviewer-agent\nspawn_notification: true\n---\n## Result\nStatus: running\n",
						)
						taskFile = lib.Task{
							TaskIdentifier: identifier,
							Frontmatter: lib.TaskFrontmatter{
								"task_identifier": "test-task-uuid-1234",
								"status":          "in_progress",
								"phase":           "human_review",
							},
							Content: lib.TaskContent(
								"## Result\nStatus: done\nNo trades found in the window.\n",
							),
						}
						Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
						written, readErr := os.ReadFile(
							filepath.Join(tmpDir, taskDir, "my-task.md"),
						)
						Expect(readErr).NotTo(HaveOccurred())
						s := string(written)
						// phase: human_review preserved from incoming
						Expect(s).To(ContainSubstring("phase: human_review"))
						// assignee cleared so operator inbox filter surfaces the task
						Expect(s).NotTo(ContainSubstring("\nassignee: pr-reviewer-agent"))
						// previous_assignee populated by clearAssignee
						Expect(s).To(ContainSubstring("previous_assignee: pr-reviewer-agent"))
						// spawn_notification key consumed by the early-return branch
						Expect(s).NotTo(ContainSubstring("spawn_notification"))
						// no escalation sections — this is a legitimate handoff, not a cap path
						Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
						Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
						// exactly one write
						Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
					},
				)
			},
		)

		Context("needs_input result", func() {
			It(
				"does not increment retry_count on Result.NextPhase=human_review handoff (legitimate handoff path)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\nretry_count: 0\n---\nOriginal body\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "human_review",
						},
						Content: lib.TaskContent("No trades found in the requested window.\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// retry_count must NOT be incremented
					Expect(s).To(ContainSubstring("retry_count: 0"))
					// Legitimate handoff: agent set Result.NextPhase=human_review; phase stays as merged.
					Expect(s).To(ContainSubstring("phase: human_review"))
					// no escalation section — this is a Done+human_review handoff, not a retry/cap path.
					Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
					Expect(s).To(ContainSubstring("No trades found"))
					// assignee cleared — task surfaces in operator inbox
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"does not increment retry_count on stale repeat publish to a task already parked at human_review (legitimate handoff stickiness)",
				func() {
					// Stale repeat publish path: task already parked at human_review from a prior Result.NextPhase handoff;
					// this publish must not bump retry_count or duplicate sections.
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: human_review\nassignee: claude\nretry_count: 2\n---\nPrevious body\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "human_review",
						},
						Content: lib.TaskContent("Another result arrives but task is terminal.\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// retry_count must remain at 2 — not incremented again
					Expect(s).To(ContainSubstring("retry_count: 2"))
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
					// assignee remains empty (already cleared by prior write)
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"preserves phase and keeps assignee cleared when deliverer published needs_input (post-spec-039 shape)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\nOriginal body\n",
					)
					// Spec 039: the deliverer's needs_input branch publishes phase unchanged
					// (lifecycle stage from the incoming frontmatter snapshot) and clears
					// assignee to "" upstream. The result writer must preserve both.
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"assignee":        "",
						},
						Content: lib.TaskContent("Needs human input.\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// Phase preserved from disk (ai_review), NOT overwritten to human_review.
					Expect(s).To(ContainSubstring("phase: ai_review"))
					Expect(s).NotTo(ContainSubstring("phase: human_review"))
					// Assignee cleared (already "" from deliverer, writer merges it as-is).
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
					Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
					// previous_assignee is NOT set because phase is ai_review (not human_review),
					// so the line-180 guard does not fire clearAssignee().
					Expect(s).NotTo(ContainSubstring("previous_assignee:"))
				},
			)

			It(
				"clears assignee via line-180 guard when agent legitimately handed off via Result.NextPhase=human_review",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\nOriginal body\n",
					)
					// Legitimate handoff: agent returned Done + NextPhase=human_review.
					// The deliverer resolveNextPhase wrote phase: human_review onto the
					// result. The writer's line-180 guard must then clear assignee.
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "human_review",
						},
						Content: lib.TaskContent("Please verify the strategy output.\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					Expect(s).NotTo(ContainSubstring("## Retry Escalation"))
					Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)
		})

		Context("trigger_count cap escalation", func() {
			It("does not escalate when trigger_count is below max_triggers", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 2\nmax_triggers: 3\nassignee: claude\n---\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"trigger_count":   2,
						"max_triggers":    3,
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: ai_review"))
				Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
			})

			It(
				"keeps phase: human_review sticky when incoming payload carries stale phase: ai_review at cap",
				func() {
					// Encodes the live dev bug: task ba1bad61 — IncrementFrontmatterExecutor
					// escalated correctly (task parked with section on disk), then agent's stale
					// result-publish clobbered phase. Disk has section → existing.Phase() restores.
					existingEscalationBody := "\n## Trigger Cap Escalation\n\n- **Timestamp:** 2026-04-18T11:00:00Z\n- **Trigger count:** 3\n- **Max triggers:** 3\n- **Assignee:** claude\n- **Last agent output:** see `## Result` above\n"
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: human_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n"+existingEscalationBody,
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review", // stale payload from agent
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "claude",
						},
						Content: lib.TaskContent(
							"## Result\nStatus: failed\nMessage: gh auth failed\n" + existingEscalationBody,
						),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("phase: ai_review"))
					Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
					Expect(strings.Count(s, "## Trigger Cap Escalation")).To(Equal(1))
					Expect(s).To(ContainSubstring("Status: failed"))
					Expect(s).To(ContainSubstring("gh auth failed"))
					Expect(s).NotTo(ContainSubstring("\nassignee: claude")) // assignee cleared
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"does not append duplicate Trigger Cap Escalation section on repeated writes at cap",
				func() {
					existingBody := "\n## Trigger Cap Escalation\n\n- **Timestamp:** 2026-04-18T11:00:00Z\n- **Trigger count:** 3\n- **Max triggers:** 3\n- **Assignee:** claude\n- **Last agent output:** see `## Result` above\n"
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: human_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n"+existingBody,
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "claude",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n" + existingBody),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(strings.Count(s, "## Trigger Cap Escalation")).To(Equal(1))
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("\nassignee: claude")) // assignee cleared
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"does not escalate when trigger_count is zero (defensive guard for new tasks)",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmax_triggers: 3\nassignee: claude\n---\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"max_triggers":    3,
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("phase: ai_review"))
					Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
				},
			)

			It(
				"keeps phase: human_review sticky despite inherited spawn_notification=true at already-parked task",
				func() {
					// Encodes the live-dev failure from task ba1bad61-5ad4-48e7-ad05-e15ba8dfbfb9
					// (controller v0.52.4, commit 1a1c570): executor's IncrementFrontmatterExecutor
					// wrote phase: human_review at cap (task parked, section on disk);
					// spawn-notification update then wrote spawn_notification: true to the file;
					// the agent's subsequent result publish called WriteResult with phase: ai_review
					// — mergeFrontmatter preserved spawn_notification: true from disk, the
					// pre-reorder applyRetryCounter hit the SpawnNotification() early return before
					// reaching the cap check, and the file landed with phase: ai_review (regression).
					// Post-reorder, the cap check runs first; existing.Phase() restores human_review.
					existingEscalationBody := "\n## Trigger Cap Escalation\n\n- **Timestamp:** 2026-04-18T11:00:00Z\n- **Trigger count:** 3\n- **Max triggers:** 3\n- **Assignee:** claude\n- **Last agent output:** see `## Result` above\n"
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: human_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\nspawn_notification: true\n---\n## Result\nStatus: failed\nMessage: previous run\n"+existingEscalationBody,
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review", // stale — agent did not observe escalation
						},
						Content: lib.TaskContent(
							"## Result\nStatus: failed\nMessage: gh auth failed\n" + existingEscalationBody,
						),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// cap enforcement must survive the inherited spawn_notification
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("phase: ai_review"))
					// trigger counts preserved
					Expect(s).To(ContainSubstring("trigger_count: 3"))
					Expect(s).To(ContainSubstring("max_triggers: 3"))
					// spawn_notification consumed (deleted) by the branch after cap enforcement
					Expect(s).NotTo(ContainSubstring("spawn_notification"))
					// escalation section present exactly once (not duplicated)
					Expect(strings.Count(s, "## Trigger Cap Escalation")).To(Equal(1))
					// agent's fresh result content is present
					Expect(s).To(ContainSubstring("Status: failed"))
					Expect(s).To(ContainSubstring("gh auth failed"))
					// assignee cleared
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"does not append duplicate Retry Escalation section on repeated writes at retry cap",
				func() {
					existingBody := "\n## Retry Escalation\n\n- **Timestamp:** 2026-04-18T11:00:00Z\n- **Attempts:** 3\n- **Assignee:** claude\n- **Last error:** see agent output above\n"
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: human_review\nretry_count: 3\nassignee: claude\n---\n"+existingBody,
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"retry_count":     3,
							"assignee":        "claude",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n" + existingBody),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(strings.Count(s, "## Retry Escalation")).To(Equal(1))
					Expect(s).To(ContainSubstring("phase: human_review"))
					Expect(s).NotTo(ContainSubstring("\nassignee: claude")) // assignee cleared
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It("writes assignee: empty and preserves phase: ai_review at trigger cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"trigger_count":   3,
						"max_triggers":    3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: ai_review"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})

			It("writes assignee: empty and preserves phase: execution at trigger cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: execution\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "execution",
						"trigger_count":   3,
						"max_triggers":    3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: execution"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})

			It("writes assignee: empty and preserves phase: planning at trigger cap", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "planning",
						"trigger_count":   3,
						"max_triggers":    3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
				written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				s := string(written)
				Expect(s).To(ContainSubstring("phase: planning"))
				Expect(s).NotTo(ContainSubstring("phase: human_review"))
				Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
				Expect(s).To(ContainSubstring("**Assignee:** claude"))
				Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
				Expect(s).To(ContainSubstring("previous_assignee: claude"))
			})

			It(
				"keeps assignee empty and phase unchanged when stale result arrives at already-parked task",
				func() {
					existingEscalationBody := "\n## Trigger Cap Escalation\n\n- **Timestamp:** 2026-04-18T11:00:00Z\n- **Trigger count:** 3\n- **Max triggers:** 3\n- **Assignee:** claude\n- **Last agent output:** see `## Result` above\n"
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: \"\"\n---\n"+existingEscalationBody,
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "planning", // stale different phase
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "claude", // stale assignee
						},
						Content: lib.TaskContent(
							"## Result\nStatus: failed\n" + existingEscalationBody,
						),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// phase must be restored to disk's ai_review (not stale planning)
					Expect(s).To(ContainSubstring("phase: ai_review"))
					Expect(s).NotTo(ContainSubstring("phase: planning"))
					// assignee must remain empty (stale claude not revived)
					Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
					// escalation section count stays at 1
					Expect(strings.Count(s, "## Trigger Cap Escalation")).To(Equal(1))
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"escalation section body records the agent name active at escalation time, not the cleared value",
				func() {
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "claude",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(
						s,
					).To(ContainSubstring("**Assignee:** claude"))
					// section text records pre-clear agent
					Expect(
						s,
					).NotTo(ContainSubstring("\nassignee: claude"))
					// frontmatter field is cleared
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
				},
			)

			It(
				"previous_assignee persists when operator re-delegates by setting a non-empty assignee",
				func() {
					// First write: trigger cap fires — assignee cleared, previous_assignee set
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "claude",
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					Expect(s).To(ContainSubstring("previous_assignee: claude"))
					Expect(
						s,
					).NotTo(ContainSubstring("\nassignee: claude"))
					// line-anchored to skip previous_assignee:

					// Second write: operator re-delegates by setting a non-empty assignee.
					// mergeFrontmatter preserves disk keys not present in incoming — previous_assignee:
					// claude from disk is kept because agent payload does not contain previous_assignee.
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "planning",
							"trigger_count":   0, // operator reset
							"max_triggers":    3,
							"assignee":        "backtest-agent", // re-delegation
						},
						Content: lib.TaskContent("## Task\nRetrying with backtest-agent.\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written2, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s2 := string(written2)
					// previous_assignee must NOT be cleared or overwritten — it persists
					Expect(s2).To(ContainSubstring("previous_assignee: claude"))
					// new assignee is set
					Expect(s2).To(ContainSubstring("assignee: backtest-agent"))
				},
			)

			It(
				"does not set previous_assignee when pre-clear assignee is already empty (defensive case)",
				func() {
					// disk: assignee already "", no previous_assignee
					writeTaskFile(
						"my-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: \"\"\n---\n## Result\nStatus: failed\n",
					)
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "in_progress",
							"phase":           "ai_review",
							"trigger_count":   3,
							"max_triggers":    3,
							"assignee":        "", // empty — malformed upstream state
						},
						Content: lib.TaskContent("## Result\nStatus: failed\n"),
					}
					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
					written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
					s := string(written)
					// agentName captured from merged.Assignee() is "", so clearAssignee skips writing previous_assignee
					Expect(s).NotTo(ContainSubstring("previous_assignee:"))
					// escalation section is still appended
					Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
				},
			)

			It("previous_assignee round-trips through YAML on a parked task", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
				)
				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
						"trigger_count":   3,
						"max_triggers":    3,
						"assignee":        "claude",
					},
					Content: lib.TaskContent("## Result\nStatus: failed\n"),
				}
				Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

				written, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(err).NotTo(HaveOccurred())

				// Parse the written file's frontmatter back into a map and assert the key
				// exists with the expected value. This exercises the YAML marshal+unmarshal
				// boundary, not just substring presence in the bytes.
				fm, fmErr := extractTestFrontmatter(string(written))
				Expect(fmErr).NotTo(HaveOccurred())
				Expect(fm["previous_assignee"]).To(Equal("claude"))
			})
		})

		Context("interleaved partial update between read and write (race-fix regression)", func() {
			It(
				"preserves a partial frontmatter update that landed between read and modify",
				func() {
					// Initial on-disk state: task in_progress, healthcheck-style probe.
					writeTaskFile(
						"probe-task.md",
						"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\nassignee: claude\ntrigger_count: 1\n---\nProbe body\n",
					)

					// Override the BeforeEach stub for this test: simulate a partial
					// update from task_update_frontmatter_executor landing on disk
					// BETWEEN the moment AtomicReadModifyWriteAndCommitPush would
					// fetch current bytes and the moment modify is invoked. We do
					// this by mutating the file inside the stub, before calling
					// modify with the freshly-re-read bytes. This is exactly what
					// git-rest does on a CAS retry: it re-reads from disk on each
					// attempt, so modify sees the interleaved write.
					fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
						ctx context.Context,
						absPath string,
						modify func([]byte) ([]byte, error),
						message string,
					) error {
						// Simulate the interleaved partial update: another writer
						// (the executor) added spawn_notification: true and bumped
						// trigger_count to 2 between our initial read and the modify
						// call.
						interleaved := "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\nassignee: claude\ntrigger_count: 2\nspawn_notification: true\n---\nProbe body\n"
						if err := os.WriteFile(absPath, []byte(interleaved), 0600); err != nil { // #nosec G306 -- test helper
							return err
						}
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

					// Agent publishes terminal completion: status: completed, phase: done.
					taskFile = lib.Task{
						TaskIdentifier: identifier,
						Frontmatter: lib.TaskFrontmatter{
							"task_identifier": "test-task-uuid-1234",
							"status":          "completed",
							"phase":           "done",
						},
						Content: lib.TaskContent("Probe completed successfully.\n"),
					}

					Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())

					written, readErr := os.ReadFile(
						filepath.Join(tmpDir, taskDir, "probe-task.md"),
					)
					Expect(readErr).NotTo(HaveOccurred())
					s := string(written)

					// 1. Agent's terminal status wins over the stale-disk in_progress.
					Expect(s).To(ContainSubstring("status: completed"))
					Expect(s).To(ContainSubstring("phase: done"))
					Expect(s).NotTo(ContainSubstring("status: in_progress"))
					Expect(s).NotTo(ContainSubstring("phase: planning"))

					// 2. The interleaved partial update's trigger_count: 2 is preserved
					//    (because modifyFn re-reads fresh disk content; the stale
					//    snapshot from FindTaskFilePath would have written trigger_count: 1
					//    and dropped the bump). This is the load-bearing assertion that
					//    proves the race is fixed.
					Expect(s).To(ContainSubstring("trigger_count: 2"))
					Expect(s).NotTo(ContainSubstring("trigger_count: 1"))

					// 3. New body fully replaces old body (WriteResult semantics).
					Expect(s).To(ContainSubstring("Probe completed successfully."))
					Expect(s).NotTo(ContainSubstring("Probe body"))

					// 4. Exactly one AtomicReadModifyWriteAndCommitPush call.
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

					// Note: do NOT assert against spawn_notification here.
					// applyRetryCounter early-returns when merged.Status() == "completed"
					// (result_writer.go:151-153) BEFORE the spawn_notification delete
					// path at line 183, so the on-disk spawn_notification: true is
					// preserved verbatim through the merge. That's a property of the
					// existing helper, not part of the race fix this test proves.
				},
			)
		})

		Context("atomic write and push error", func() {
			It("returns error when AtomicWriteAndCommitPush fails", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: open\n---\nOld content\n",
				)
				fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
					ctx context.Context,
					absPath string,
					modify func([]byte) ([]byte, error),
					message string,
				) error {
					return errTest
				}

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "done",
					},
					Content: lib.TaskContent("Result\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).To(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
			})
		})

		Context("Review section preservation", func() {
			// PIt: test documents the known bug — ## Review content is currently stripped.
			// Remove the P prefix once WriteResult is updated to preserve existing ## Review sections.
			PIt("preserves prior ## Review content when writing a new result", func() {
				writeTaskFile(
					"my-task.md",
					"---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nassignee: claude\n---\n# Body\n## Review\nPrior review content\n",
				)

				taskFile = lib.Task{
					TaskIdentifier: identifier,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": "test-task-uuid-1234",
						"status":          "in_progress",
						"phase":           "ai_review",
					},
					Content: lib.TaskContent("# Body\n\n## Review\nNew review content\n"),
				}

				err := writer.WriteResult(ctx, taskFile)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
				// Read the actual file the stub wrote.
				written, readErr := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
				Expect(readErr).NotTo(HaveOccurred())
				s := string(written)
				// Either the prior review survives in-place, OR it survives under an
				// "## Outdated by force-push" marker. NEVER stripped silently.
				Expect(s).To(SatisfyAny(
					ContainSubstring("Prior review content"),
					ContainSubstring("## Outdated by"),
				))
			})
		})
	})

	Describe("FindTaskFilePath", func() {
		It("calls gitClient.ListFiles + ReadFile with the expected glob and matched paths", func() {
			fakeGC := &mocks.GitClient{}
			fakeGC.ListFilesReturns([]string{"tasks/a.md", "tasks/b.md"}, nil)
			fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: foo\n---\n"), nil)
			fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: bar\n---\n"), nil)

			matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "bar")
			Expect(err).NotTo(HaveOccurred())
			Expect(matchedRelPath).To(Equal("tasks/b.md"))
			Expect(fakeGC.ListFilesCallCount()).To(Equal(1))
			_, glob := fakeGC.ListFilesArgsForCall(0)
			Expect(glob).To(Equal("tasks/*.md"))
			Expect(fakeGC.ReadFileCallCount()).To(BeNumerically(">=", 1))
		})

		It("returns empty path when no file matches", func() {
			fakeGC := &mocks.GitClient{}
			fakeGC.ListFilesReturns([]string{"tasks/a.md"}, nil)
			fakeGC.ReadFileReturnsOnCall(0, []byte("---\ntask_identifier: other\n---\n"), nil)

			matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "missing")
			Expect(err).NotTo(HaveOccurred())
			Expect(matchedRelPath).To(Equal(""))
			Expect(fm).To(BeNil())
		})

		It("skips files that fail to read", func() {
			fakeGC := &mocks.GitClient{}
			fakeGC.ListFilesReturns([]string{"tasks/bad.md", "tasks/good.md"}, nil)
			fakeGC.ReadFileReturnsOnCall(0, nil, errTest)
			fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: target\n---\n"), nil)

			matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "target")
			Expect(err).NotTo(HaveOccurred())
			Expect(matchedRelPath).To(Equal("tasks/good.md"))
		})

		It("skips files with invalid frontmatter", func() {
			fakeGC := &mocks.GitClient{}
			fakeGC.ListFilesReturns([]string{"tasks/bad.md", "tasks/good.md"}, nil)
			fakeGC.ReadFileReturnsOnCall(0, []byte("no frontmatter here"), nil)
			fakeGC.ReadFileReturnsOnCall(1, []byte("---\ntask_identifier: target\n---\n"), nil)

			matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "target")
			Expect(err).NotTo(HaveOccurred())
			Expect(matchedRelPath).To(Equal("tasks/good.md"))
		})

		It("returns error when ListFiles fails", func() {
			fakeGC := &mocks.GitClient{}
			fakeGC.ListFilesReturns(nil, errTest)

			_, _, err := result.FindTaskFilePath(ctx, fakeGC, "tasks", "any")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ExtractBody", func() {
		It("returns body after frontmatter delimiter", func() {
			content := []byte("---\nkey: value\n---\nbody content here\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal("body content here\n"))
		})

		It("returns empty string when body is empty", func() {
			content := []byte("---\nkey: value\n---\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal(""))
		})

		It("returns error when no frontmatter delimiter", func() {
			content := []byte("no delimiter here")
			_, err := result.ExtractBody(ctx, content)
			Expect(err).To(HaveOccurred())
		})

		It("returns error when frontmatter is not closed", func() {
			content := []byte("---\nkey: value\n")
			_, err := result.ExtractBody(ctx, content)
			Expect(err).To(HaveOccurred())
		})

		It("handles CRLF line endings after opening delimiter", func() {
			content := []byte("---\r\nkey: value\n---\nbody\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal("body\n"))
		})
	})
})
