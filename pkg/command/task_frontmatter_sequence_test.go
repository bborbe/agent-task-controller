// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("Frontmatter sequence integration", func() {
	var (
		ctx           context.Context
		tmpDir        string
		taskDir       string
		fakeGit       *mocks.GitClient
		incrementExec cdb.CommandObjectExecutorTx
		updateExec    cdb.CommandObjectExecutorTx
		schemaID      cdb.SchemaID
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "fm-sequence-test-*")
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

		incrementExec = command.NewIncrementFrontmatterExecutor(fakeGit, taskDir)
		updateExec = command.NewUpdateFrontmatterExecutor(fakeGit, taskDir)
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

	buildIncrementCmdObj := func(cmd task.IncrementFrontmatterCommand) cdb.CommandObject {
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

	buildUpdateCmdObj := func(cmd task.UpdateFrontmatterCommand) cdb.CommandObject {
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

	Context("Test A — increment then spawn-notification preserves trigger_count", func() {
		It(
			"does not reset trigger_count when UpdateFrontmatterCommand sets spawn-notification keys",
			func() {
				taskFile := writeTaskFile(
					"seq-test-001.md",
					"---\ntask_identifier: seq-test-001\ntrigger_count: 0\nmax_triggers: 3\nstatus: in_progress\nphase: ai_review\n---\nbody\n",
				)

				// Step 1: increment trigger_count 0 -> 1
				_, _, err := incrementExec.HandleCommand(
					ctx,
					nil,
					buildIncrementCmdObj(task.IncrementFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("seq-test-001"),
						Field:          "trigger_count",
						Delta:          1,
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				fm := parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 1))

				// Step 2: spawn-notification update — must NOT touch trigger_count
				_, _, err = updateExec.HandleCommand(
					ctx,
					nil,
					buildUpdateCmdObj(task.UpdateFrontmatterCommand{
						TaskIdentifier: lib.TaskIdentifier("seq-test-001"),
						Updates: lib.TaskFrontmatter{
							"current_job":        "claude-20260424120000",
							"job_started_at":     "2026-04-24T12:00:00Z",
							"spawn_notification": true,
						},
					}),
				)
				Expect(err).NotTo(HaveOccurred())

				fm = parseFrontmatter(taskFile)
				Expect(fm["trigger_count"]).To(BeNumerically("==", 1))
				Expect(fm["max_triggers"]).To(BeNumerically("==", 3))
				Expect(fm["current_job"]).To(Equal("claude-20260424120000"))
				Expect(fm["job_started_at"]).To(Equal("2026-04-24T12:00:00Z"))
				Expect(fm["spawn_notification"]).To(BeTrue())
				Expect(fm["status"]).To(Equal("in_progress"))
				Expect(fm["phase"]).To(Equal("ai_review"))
			},
		)
	})

	Context("Test B — increment then failure-update preserves trigger_count", func() {
		It("does not reset trigger_count when UpdateFrontmatterCommand sets failure keys", func() {
			taskFile := writeTaskFile(
				"seq-test-002.md",
				"---\ntask_identifier: seq-test-002\ntrigger_count: 2\nmax_triggers: 3\nstatus: in_progress\nphase: ai_review\ncurrent_job: claude-old-job\n---\nbody\n",
			)

			// Failure update — must NOT touch trigger_count
			_, _, err := updateExec.HandleCommand(
				ctx,
				nil,
				buildUpdateCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("seq-test-002"),
					Updates: lib.TaskFrontmatter{
						"status":      "in_progress",
						"phase":       "ai_review",
						"current_job": "",
					},
				}),
			)
			Expect(err).NotTo(HaveOccurred())

			fm := parseFrontmatter(taskFile)
			Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
			Expect(fm["status"]).To(Equal("in_progress"))
			Expect(fm["phase"]).To(Equal("ai_review"))
			Expect(fm["current_job"]).To(Equal(""))
		})
	})

	Context("Test C — UpdateFrontmatterCommand with empty Updates is a no-op", func() {
		It("leaves the file unchanged when Updates is nil", func() {
			taskFile := writeTaskFile(
				"seq-test-003.md",
				"---\ntask_identifier: seq-test-003\ntrigger_count: 1\nstatus: in_progress\n---\nbody\n",
			)

			_, _, err := updateExec.HandleCommand(
				ctx,
				nil,
				buildUpdateCmdObj(task.UpdateFrontmatterCommand{
					TaskIdentifier: lib.TaskIdentifier("seq-test-003"),
					Updates:        nil,
				}),
			)
			Expect(err).NotTo(HaveOccurred())

			fm := parseFrontmatter(taskFile)
			Expect(fm["trigger_count"]).To(BeNumerically("==", 1))
			Expect(fm["status"]).To(Equal("in_progress"))
		})
	})
})

var _ = Describe("domain.NormalizeTaskPhase alias (spec 038)", func() {
	It("normalizes legacy phase 'in_progress' to TaskPhaseExecution", func() {
		canonical, ok := domain.NormalizeTaskPhase("in_progress")
		Expect(ok).To(BeTrue())
		Expect(canonical).To(Equal(domain.TaskPhaseExecution))
	})
})
