// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent/lib"
	task "github.com/bborbe/agent/lib/command/task"
	"github.com/bborbe/agent/task/controller/mocks"
	"github.com/bborbe/agent/task/controller/pkg/command"
)

var _ = Describe("NewIncrementFrontmatterExecutor", func() {
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
		tmpDir, err = os.MkdirTemp("", "increment-fm-test-*")
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
		// Wire AtomicReadModifyWriteAndCommitPush to actually call the modify func and write the file
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

		executor = command.NewIncrementFrontmatterExecutor(fakeGit, taskDir)
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

	buildCmdObj := func(cmd task.IncrementFrontmatterCommand) cdb.CommandObject {
		event, err := base.ParseEvent(ctx, cmd)
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				RequestID: base.NewRequestID(),
				Operation: command.IncrementFrontmatterCommandOperation,
				Initiator: "test",
				Data:      event,
			},
			SchemaID: schemaID,
		}
	}

	Describe("CommandOperation", func() {
		It("returns increment-frontmatter", func() {
			Expect(
				executor.CommandOperation(),
			).To(Equal(base.CommandOperation("increment-frontmatter")))
		})
	})

	Describe("HandleCommand", func() {
		Context("monotonic increment across sequential commands", func() {
			It("produces value 2 after two increment-by-1 commands", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: inc-test-uuid\ntrigger_count: 0\n---\nbody\n",
				)
				cmd1 := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("inc-test-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd1)
				Expect(err).NotTo(HaveOccurred())

				// Simulate second command: stub now reads updated file
				cmd2 := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("inc-test-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err = executor.HandleCommand(ctx, nil, cmd2)
				Expect(err).NotTo(HaveOccurred())

				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
			})
		})

		Context("phase escalation at cap", func() {
			It(
				"clears assignee and preserves phase when trigger_count reaches max_triggers",
				func() {
					taskFile := writeTaskFile(
						"task.md",
						"---\ntask_identifier: cap-test-uuid\ntrigger_count: 0\nmax_triggers: 2\n---\nbody\n",
					)
					// First increment: 0 -> 1, no escalation
					cmd1 := buildCmdObj(task.IncrementFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("cap-test-uuid"),
						Field:          "trigger_count",
						Delta:          1,
					})
					_, _, err := executor.HandleCommand(ctx, nil, cmd1)
					Expect(err).NotTo(HaveOccurred())
					fm := parseFrontmatter(taskFile)
					Expect(fm["trigger_count"]).To(BeNumerically("==", 1))
					Expect(fm["phase"]).To(BeNil())

					// Second increment: 1 -> 2, escalation fires
					cmd2 := buildCmdObj(task.IncrementFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("cap-test-uuid"),
						Field:          "trigger_count",
						Delta:          1,
					})
					_, _, err = executor.HandleCommand(ctx, nil, cmd2)
					Expect(err).NotTo(HaveOccurred())
					fm = parseFrontmatter(taskFile)
					Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
					Expect(fm["phase"]).NotTo(Equal("human_review"))
					Expect(fm["assignee"]).To(BeEmpty())
				},
			)

			It("preserves phase: planning when trigger_count reaches max_triggers", func() {
				taskFile := writeTaskFile(
					"cap-planning.md",
					"---\ntask_identifier: cap-planning-uuid\ntrigger_count: 1\nmax_triggers: 2\nphase: planning\nassignee: some-agent\n---\nbody\n",
				)
				cmd := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("cap-planning-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["phase"]).To(Equal("planning"))
				Expect(fm["phase"]).NotTo(Equal("human_review"))
				Expect(fm["assignee"]).To(BeEmpty())
			})

			It("preserves phase: in_progress when trigger_count reaches max_triggers", func() {
				taskFile := writeTaskFile(
					"cap-in-progress.md",
					"---\ntask_identifier: cap-in-progress-uuid\ntrigger_count: 1\nmax_triggers: 2\nphase: in_progress\nassignee: some-agent\n---\nbody\n",
				)
				cmd := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("cap-in-progress-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["phase"]).To(Equal("in_progress"))
				Expect(fm["phase"]).NotTo(Equal("human_review"))
				Expect(fm["assignee"]).To(BeEmpty())
			})

			It("preserves phase: ai_review when trigger_count reaches max_triggers", func() {
				taskFile := writeTaskFile(
					"cap-ai-review.md",
					"---\ntask_identifier: cap-ai-review-uuid\ntrigger_count: 1\nmax_triggers: 2\nphase: ai_review\nassignee: some-agent\n---\nbody\n",
				)
				cmd := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("cap-ai-review-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["phase"]).To(Equal("ai_review"))
				Expect(fm["phase"]).NotTo(Equal("human_review"))
				Expect(fm["assignee"]).To(BeEmpty())
			})
		})

		Context("no escalation below cap", func() {
			It("does not set human_review when trigger_count < max_triggers", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: nocap-uuid\ntrigger_count: 1\nmax_triggers: 3\nphase: ai_review\n---\nbody\n",
				)
				cmd := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("nocap-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["phase"]).To(Equal("ai_review"))
			})
		})

		Context("field not present", func() {
			It("treats missing field as 0 and sets value to delta", func() {
				taskFile := writeTaskFile(
					"task.md",
					"---\ntask_identifier: missing-field-uuid\n---\nbody\n",
				)
				cmd := buildCmdObj(task.IncrementFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("missing-field-uuid"),
					Field:          "trigger_count",
					Delta:          1,
				})
				_, _, err := executor.HandleCommand(ctx, nil, cmd)
				Expect(err).NotTo(HaveOccurred())
				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 1))
			})
		})

		Context("task not found", func() {
			It("returns nil without writing when no matching file exists", func() {
				// No task file written
				_, _, err := executor.HandleCommand(
					ctx,
					nil,
					buildCmdObj(task.IncrementFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("nonexistent-uuid"),
						Field:          "trigger_count",
						Delta:          1,
					}),
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
		})
	})
})
