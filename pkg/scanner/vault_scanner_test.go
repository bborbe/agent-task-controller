// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner_test

// NOTE: testGitClient is a hand-written test double rather than a Counterfeiter
// mock because importing the mocks package would create an import cycle:
// mocks/ imports scanner for scanner.ScanResult, so scanner cannot import mocks.
// This is a documented exception to the Counterfeiter-mocks rule.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent/task/controller/pkg/metrics"
	"github.com/bborbe/agent/task/controller/pkg/scanner"
)

func extractFrontmatterStr(fileContent string) string {
	const pfx = "---\n"
	if !strings.HasPrefix(fileContent, pfx) {
		return ""
	}
	rest := fileContent[len(pfx):]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return ""
	}
	return rest[:idx]
}

type testGitClient struct {
	path          string
	pullErr       error
	commitPushErr error
}

func (t *testGitClient) EnsureCloned(_ context.Context) error { return nil }

func (t *testGitClient) Pull(_ context.Context) error { return t.pullErr }

func (t *testGitClient) Path() string { return t.path }

func (t *testGitClient) CommitAndPush(_ context.Context, _ string) error {
	return t.commitPushErr
}

func (t *testGitClient) AtomicWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return t.commitPushErr
}

func (t *testGitClient) AtomicReadModifyWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ func([]byte) ([]byte, error),
	_ string,
) error {
	return t.commitPushErr
}

func (t *testGitClient) ListFiles(_ context.Context, _ string) ([]string, error) { return nil, nil }

func (t *testGitClient) ReadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func (t *testGitClient) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

// fileOpsTestGitClient is a test double with real file I/O for NewGitRestVaultScanner tests.
type fileOpsTestGitClient struct {
	path          string
	pullErr       error
	commitPushErr error
}

func (t *fileOpsTestGitClient) EnsureCloned(_ context.Context) error { return nil }

func (t *fileOpsTestGitClient) Pull(_ context.Context) error { return t.pullErr }

func (t *fileOpsTestGitClient) Path() string { return t.path }

func (t *fileOpsTestGitClient) CommitAndPush(_ context.Context, _ string) error {
	return t.commitPushErr
}

func (t *fileOpsTestGitClient) AtomicWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return t.commitPushErr
}

func (t *fileOpsTestGitClient) AtomicReadModifyWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ func([]byte) ([]byte, error),
	_ string,
) error {
	return t.commitPushErr
}

func (t *fileOpsTestGitClient) ListFiles(_ context.Context, glob string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(t.path, glob))
	if err != nil {
		return nil, err
	}
	var rel []string
	for _, m := range matches {
		r, _ := filepath.Rel(t.path, m)
		rel = append(rel, r)
	}
	return rel, nil
}

func (t *fileOpsTestGitClient) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(t.path, relPath)) // #nosec G304 -- test-only path
}

func (t *fileOpsTestGitClient) WriteFile(_ context.Context, relPath string, content []byte) error {
	return os.WriteFile(filepath.Join(t.path, relPath), content, 0600)
}

// failingReadFileOpsGitClient shadows ReadFile to always fail while keeping
// the real ListFiles implementation from fileOpsTestGitClient.
type failingReadFileOpsGitClient struct {
	*fileOpsTestGitClient
}

func (f *failingReadFileOpsGitClient) ReadFile(_ context.Context, _ string) ([]byte, error) {
	return nil, os.ErrNotExist
}

func mustInitGitRepo(dir string) {
	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		// #nosec G204 -- test helper: commands are hardcoded test setup git invocations
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		Expect(err).To(BeNil(), "cmd %v failed: %s", args, string(out))
	}
}

var _ = Describe("VaultScanner", func() {
	var (
		ctx     context.Context
		s       scanner.VaultScanner
		tmpDir  string
		taskDir string
		fakeGit *testGitClient
		results chan scanner.ScanResult
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "scanner-test-*")
		Expect(err).To(BeNil())
		taskDir = "24 Tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())
		mustInitGitRepo(tmpDir)

		fakeGit = &testGitClient{path: tmpDir}
		results = make(chan scanner.ScanResult, 1)

		s = scanner.NewVaultScanner(
			fakeGit,
			taskDir,
			time.Second,
			make(chan struct{}),
			metrics.New(),
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	Describe("assignee transition → counter reset", func() {
		It(
			"resets trigger_count and retry_count when assignee transitions from empty to named",
			func() {
				taskID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
				absPath := filepath.Join(tmpDir, taskDir, "parked.md")
				initialContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: \ntrigger_count: 3\nretry_count: 2\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(initialContent), 0600)).To(Succeed())

				// First scan: parked (assignee empty), no task published, no reset
				s.RunCycle(ctx, results)
				var r1 scanner.ScanResult
				Expect(results).To(Receive(&r1))
				Expect(r1.Changed).To(BeEmpty())

				// Operator re-delegates: set assignee to claude
				delegatedContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claude\ntrigger_count: 3\nretry_count: 2\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(delegatedContent), 0600)).To(Succeed())

				// Second scan: detects transition, writes reset, no task published yet (zero-hash sentinel)
				s.RunCycle(ctx, results)
				var r2 scanner.ScanResult
				Expect(results).To(Receive(&r2))
				Expect(r2.Changed).To(BeEmpty())

				// Verify the file was rewritten with reset counters
				written, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				var fm map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(written))), &fm),
				).To(Succeed())
				Expect(fm["trigger_count"]).To(BeNumerically("==", 0))
				Expect(fm["retry_count"]).To(BeNumerically("==", 0))
				Expect(fm["assignee"]).To(Equal("claude"))

				// Third scan: reads reset file, publishes task with fresh counters
				s.RunCycle(ctx, results)
				var r3 scanner.ScanResult
				Expect(results).To(Receive(&r3))
				Expect(r3.Changed).To(HaveLen(1))
				Expect(string(r3.Changed[0].Frontmatter.Assignee())).To(Equal("claude"))
				Expect(r3.Changed[0].Frontmatter.TriggerCount()).To(Equal(0))
				Expect(r3.Changed[0].Frontmatter.RetryCount()).To(Equal(0))
			},
		)

		It(
			"does not reset counters when assignee changes from one name to another (named → named)",
			func() {
				taskID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
				absPath := filepath.Join(tmpDir, taskDir, "named-named.md")
				initialContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claudeA\ntrigger_count: 2\nretry_count: 1\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(initialContent), 0600)).To(Succeed())

				// First scan: task published with claudeA
				s.RunCycle(ctx, results)
				Expect(results).To(Receive())

				// Operator changes to claudeB
				changedContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claudeB\ntrigger_count: 2\nretry_count: 1\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(changedContent), 0600)).To(Succeed())

				// Second scan: named → named, no reset, task published with new assignee
				s.RunCycle(ctx, results)
				var r2 scanner.ScanResult
				Expect(results).To(Receive(&r2))
				Expect(r2.Changed).To(HaveLen(1))

				// Counters must NOT have been reset
				written, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				var fm map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(written))), &fm),
				).To(Succeed())
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["retry_count"]).To(BeNumerically("==", 1))
			},
		)

		It(
			"does not reset counters when assignee is cleared from named to empty (named → empty)",
			func() {
				taskID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
				absPath := filepath.Join(tmpDir, taskDir, "named-empty.md")
				initialContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claude\ntrigger_count: 2\nretry_count: 1\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(initialContent), 0600)).To(Succeed())

				// First scan: task published with claude
				s.RunCycle(ctx, results)
				Expect(results).To(Receive())

				// Operator clears assignee
				clearedContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: \ntrigger_count: 2\nretry_count: 1\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(clearedContent), 0600)).To(Succeed())

				// Second scan: named → empty, no reset, task skipped (empty assignee)
				s.RunCycle(ctx, results)
				var r2 scanner.ScanResult
				Expect(results).To(Receive(&r2))
				Expect(r2.Changed).To(BeEmpty())

				// File counters must NOT have changed
				written, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				var fm map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(written))), &fm),
				).To(Succeed())
				Expect(fm["trigger_count"]).To(BeNumerically("==", 2))
				Expect(fm["retry_count"]).To(BeNumerically("==", 1))
			},
		)

		It(
			"emits counter reset exactly once even if the same empty→named transition is observed twice in consecutive scans",
			func() {
				taskID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
				absPath := filepath.Join(tmpDir, taskDir, "idempotent.md")
				initialContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: \ntrigger_count: 3\nretry_count: 2\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(initialContent), 0600)).To(Succeed())

				// First scan: parked, no task
				s.RunCycle(ctx, results)
				Expect(results).To(Receive())

				// Operator re-delegates
				delegatedContent := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claude\ntrigger_count: 3\nretry_count: 2\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(delegatedContent), 0600)).To(Succeed())

				// Second scan: detects transition, writes reset once
				s.RunCycle(ctx, results)
				Expect(results).To(Receive())

				// Capture file state after first reset
				afterFirstReset, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				var fm1 map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(afterFirstReset))), &fm1),
				).To(Succeed())
				Expect(fm1["trigger_count"]).To(BeNumerically("==", 0))
				Expect(fm1["retry_count"]).To(BeNumerically("==", 0))

				// Third scan: zero-hash sentinel forces re-process, but prevEntry.assignee = "claude"
				// → no second transition detected → no second reset
				s.RunCycle(ctx, results)
				Expect(results).To(Receive())

				// File content must be byte-identical to post-first-reset (no second write)
				afterSecondScan, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				Expect(afterSecondScan).To(Equal(afterFirstReset))

				var fm2 map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(afterSecondScan))), &fm2),
				).To(Succeed())
				Expect(fm2["trigger_count"]).To(BeNumerically("==", 0))
				Expect(fm2["retry_count"]).To(BeNumerically("==", 0))
			},
		)

		It(
			"does not reset counters on first scan of a file that already has a non-empty assignee",
			func() {
				taskID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
				absPath := filepath.Join(tmpDir, taskDir, "already-assigned.md")
				content := "---\ntask_identifier: " + taskID + "\nstatus: in_progress\nphase: ai_review\nassignee: claude\ntrigger_count: 5\nretry_count: 4\n---\n# body\n"
				Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

				// First scan ever — prevEntry.taskIdentifier == "" (zero value)
				s.RunCycle(ctx, results)
				var r1 scanner.ScanResult
				Expect(results).To(Receive(&r1))
				Expect(r1.Changed).To(HaveLen(1))

				// Counters must NOT have been reset
				written, _ := os.ReadFile(absPath) // #nosec G304 -- test-only path
				var fm map[string]interface{}
				Expect(
					yaml.Unmarshal([]byte(extractFrontmatterStr(string(written))), &fm),
				).To(Succeed())
				Expect(fm["trigger_count"]).To(BeNumerically("==", 5))
				Expect(fm["retry_count"]).To(BeNumerically("==", 4))
			},
		)
	})

	Describe("processFile edge cases", func() {
		It("processes file with duplicate task_identifier (last wins)", func() {
			content := "---\ntask_identifier: 11111111-1111-4111-8111-111111111121\nstatus: todo\nassignee: claude\ntask_identifier: 22222222-2222-4222-8222-222222222221\n---\n# Dup task"
			absPath := filepath.Join(tmpDir, taskDir, "dup-key.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(
				string(result.Changed[0].TaskIdentifier),
			).To(Equal("22222222-2222-4222-8222-222222222221"))
		})

		It("passes through file with non-empty unknown status", func() {
			content := "---\ntask_identifier: 55555555-5555-4555-8555-555555555555\nstatus: definitely_invalid_status\nassignee: claude\n---\n"
			absPath := filepath.Join(tmpDir, taskDir, "bad-status.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(
				string(result.Changed[0].Frontmatter.Status()),
			).To(Equal("definitely_invalid_status"))
		})

		It("handles CRLF line endings in full cycle", func() {
			content := "---\r\ntask_identifier: 66666666-6666-4666-8666-666666666666\r\nstatus: todo\r\nassignee: claude\r\n---\r\n# Task"
			absPath := filepath.Join(tmpDir, taskDir, "crlf-cycle.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].Frontmatter.Assignee())).To(Equal("claude"))
		})

		It(
			"skips file when YAML is invalid and fails DeduplicateFrontmatter first unmarshal",
			func() {
				// Content with a bare '[' character is invalid YAML (unclosed flow sequence).
				// DeduplicateFrontmatter returns an error, causing processFile to skip the file.
				// RunCycle always sends an empty ScanResult to the channel, so we verify
				// that Changed is empty (file was not published).
				content := "---\ntask_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: [invalid\n---\n# Invalid"
				absPath := filepath.Join(tmpDir, taskDir, "invalid-yaml-frontmatter.md")
				Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

				s.RunCycle(ctx, results)
				var result scanner.ScanResult
				Expect(results).To(Receive(&result))
				Expect(result.Changed).To(BeEmpty())
			},
		)
	})

	Describe("DeduplicateFrontmatter", func() {
		It("returns original YAML unchanged when no duplicates", func() {
			input := "task_identifier: abc-123\nstatus: todo\nassignee: claude\n"
			out, hasDup, err := scanner.DeduplicateFrontmatter(ctx, input)
			Expect(err).To(BeNil())
			Expect(hasDup).To(BeFalse())
			Expect(out).To(Equal(input))
		})

		It("deduplicates a single repeated key, last value wins", func() {
			input := "task_identifier: first\nstatus: next\ntask_identifier: second\n"
			out, hasDup, err := scanner.DeduplicateFrontmatter(ctx, input)
			Expect(err).To(BeNil())
			Expect(hasDup).To(BeTrue())
			var result map[string]interface{}
			Expect(yaml.Unmarshal([]byte(out), &result)).To(Succeed())
			Expect(result["task_identifier"]).To(Equal("second"))
			Expect(result["status"]).To(Equal("next"))
		})

		It("deduplicates multiple repeated keys, last value wins each", func() {
			input := "task_identifier: first\nstatus: todo\ntask_identifier: second\nstatus: in_progress\n"
			out, hasDup, err := scanner.DeduplicateFrontmatter(ctx, input)
			Expect(err).To(BeNil())
			Expect(hasDup).To(BeTrue())
			var result map[string]interface{}
			Expect(yaml.Unmarshal([]byte(out), &result)).To(Succeed())
			Expect(result["task_identifier"]).To(Equal("second"))
			Expect(result["status"]).To(Equal("in_progress"))
		})
	})

	Describe("InjectTaskIdentifier", func() {
		It("injects task_identifier with LF line endings", func() {
			input := []byte("---\nstatus: todo\n---\n")
			result, err := scanner.InjectTaskIdentifier(context.Background(), input, "test-id")
			Expect(err).To(BeNil())
			Expect(string(result)).To(Equal("---\ntask_identifier: test-id\nstatus: todo\n---\n"))
		})

		It("injects task_identifier with CRLF line endings", func() {
			input := []byte("---\r\nstatus: todo\r\n---\r\n")
			result, err := scanner.InjectTaskIdentifier(context.Background(), input, "test-id")
			Expect(err).To(BeNil())
			Expect(
				string(result),
			).To(Equal("---\r\ntask_identifier: test-id\r\nstatus: todo\r\n---\r\n"))
		})

		It("returns error when content does not start with frontmatter delimiter", func() {
			input := []byte("no frontmatter")
			result, err := scanner.InjectTaskIdentifier(context.Background(), input, "test-id")
			Expect(err).NotTo(BeNil())
			Expect(result).To(BeNil())
		})
	})

	Describe("NewVaultScanner", func() {
		It("returns a non-nil VaultScanner", func() {
			vs := scanner.NewVaultScanner(fakeGit, taskDir, time.Hour, nil, metrics.New())
			Expect(vs).NotTo(BeNil())
		})
	})

	Describe("NewGitRestVaultScanner", func() {
		It("uses fileOps (ListFiles/ReadFile/WriteFile) through the fileOps interface", func() {
			// fileOpsTestGitClient provides real ListFiles/ReadFile/WriteFile implementations
			gitClient := &fileOpsTestGitClient{path: tmpDir}
			vs := scanner.NewGitRestVaultScanner(gitClient, taskDir, time.Hour, nil, metrics.New())
			Expect(vs).NotTo(BeNil())

			// Write a task file and run a cycle to exercise ListFiles/ReadFile/WriteFile
			content := "---\ntask_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: claude\n---\n# Test"
			absPath := filepath.Join(tmpDir, taskDir, "gitrest-cycle.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			resultsCh := make(chan scanner.ScanResult, 1)
			vs.RunCycle(ctx, resultsCh)

			var result scanner.ScanResult
			Expect(resultsCh).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].Frontmatter.Assignee())).To(Equal("claude"))
		})
	})

	Describe("Run", func() {
		It("returns nil when context is cancelled", func() {
			vs := scanner.NewVaultScanner(fakeGit, taskDir, time.Hour, nil, metrics.New())
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				done <- vs.Run(runCtx, make(chan scanner.ScanResult, 1))
			}()
			cancel()
			Eventually(done, 200*time.Millisecond).Should(Receive(BeNil()))
		})

		It("runs cycle when trigger fires", func() {
			content := "---\ntask_identifier: 44444444-4444-4444-8444-444444444444\nstatus: todo\nassignee: claude\n---\n# Triggered task"
			absPath := filepath.Join(tmpDir, taskDir, "trigger-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			trigger := make(chan struct{}, 1)
			vs := scanner.NewVaultScanner(fakeGit, taskDir, time.Hour, trigger, metrics.New())
			scanResults := make(chan scanner.ScanResult, 1)
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				done <- vs.Run(runCtx, scanResults)
			}()

			trigger <- struct{}{}

			var result scanner.ScanResult
			Eventually(scanResults, time.Second).Should(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(
				string(result.Changed[0].TaskIdentifier),
			).To(Equal("44444444-4444-4444-8444-444444444444"))

			cancel()
			Eventually(done, 200*time.Millisecond).Should(Receive(BeNil()))
		})
	})

	Describe("runCycle", func() {
		It("new file appears in Changed", func() {
			content := "---\ntask_identifier: 11111111-1111-4111-8111-111111111111\nstatus: todo\nassignee: claude\n---\n# New task"
			absPath := filepath.Join(tmpDir, taskDir, "new-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)

			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].Frontmatter.Assignee())).To(Equal("claude"))
		})

		It("unchanged file is not in Changed on second cycle", func() {
			content := "---\ntask_identifier: 11111111-1111-4111-8111-111111111112\nstatus: todo\nassignee: claude\n---\n# Stable task"
			absPath := filepath.Join(tmpDir, taskDir, "stable-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var first scanner.ScanResult
			Expect(results).To(Receive(&first))
			Expect(first.Changed).To(HaveLen(1))

			s.RunCycle(ctx, results)
			var second scanner.ScanResult
			Expect(results).To(Receive(&second))
			Expect(second.Changed).To(BeEmpty())
		})

		It("modified file appears in Changed on next cycle", func() {
			content := "---\ntask_identifier: 22222222-2222-4222-8222-222222222222\nstatus: todo\nassignee: claude\n---\n# Original"
			absPath := filepath.Join(tmpDir, taskDir, "modified-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			Expect(results).To(Receive())

			updated := "---\ntask_identifier: 22222222-2222-4222-8222-222222222222\nstatus: in_progress\nassignee: claude\n---\n# Updated"
			Expect(os.WriteFile(absPath, []byte(updated), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].Frontmatter.Status())).To(Equal("in_progress"))
		})

		It("drops result when channel is already full", func() {
			// Pre-fill the results channel (capacity 1)
			results <- scanner.ScanResult{}

			content := "---\ntask_identifier: 11111111-1111-4111-8111-111111111113\nstatus: todo\nassignee: claude\n---\n# Task"
			absPath := filepath.Join(tmpDir, taskDir, "drop-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			// runCycle should not block even though channel is full
			s.RunCycle(ctx, results)
			// drain the pre-filled result (not the one we just tried to send)
			Expect(results).To(Receive())
		})

		It("skips cycle when git pull fails", func() {
			fakeGit.pullErr = context.DeadlineExceeded

			content := "---\ntask_identifier: 11111111-1111-4111-8111-111111111114\nstatus: todo\nassignee: claude\n---\n# Task"
			absPath := filepath.Join(tmpDir, taskDir, "pull-fail.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			Consistently(results, 50*time.Millisecond).ShouldNot(Receive())
		})

		It("deleted file appears in Deleted on next cycle", func() {
			content := "---\ntask_identifier: 33333333-3333-4333-8333-333333333333\nstatus: todo\nassignee: claude\n---\n# Task"
			absPath := filepath.Join(tmpDir, taskDir, "deleted-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			Expect(results).To(Receive())

			Expect(os.Remove(absPath)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Deleted).To(HaveLen(1))
			Expect(string(result.Deleted[0])).To(Equal("33333333-3333-4333-8333-333333333333"))
		})

		It("UUID injected when task_identifier absent", func() {
			content := "---\nstatus: todo\nassignee: claude\n---\n# Task without UUID"
			absPath := filepath.Join(tmpDir, taskDir, "no-uuid-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)

			// Not published this cycle (write-back happened)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(BeEmpty())

			// File on disk now contains task_identifier
			written, err := os.ReadFile(absPath) // #nosec G304 -- test-only path
			Expect(err).To(BeNil())
			Expect(string(written)).To(ContainSubstring("task_identifier:"))
		})

		It("task published on second cycle after injection", func() {
			content := "---\nstatus: todo\nassignee: claude\n---\n# Task without UUID"
			absPath := filepath.Join(tmpDir, taskDir, "no-uuid-task2.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			// First cycle: injects UUID, does not publish
			s.RunCycle(ctx, results)
			var first scanner.ScanResult
			Expect(results).To(Receive(&first))
			Expect(first.Changed).To(BeEmpty())

			// Second cycle: publishes with UUID
			s.RunCycle(ctx, results)
			var second scanner.ScanResult
			Expect(results).To(Receive(&second))
			Expect(second.Changed).To(HaveLen(1))
			Expect(
				string(second.Changed[0].TaskIdentifier),
			).To(MatchRegexp(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`))
		})

		It("non-UUID task_identifier is replaced with generated UUID", func() {
			content := "---\ntask_identifier: my-custom-id\nstatus: todo\nassignee: claude\n---\n# Task with non-UUID id"
			absPath := filepath.Join(tmpDir, taskDir, "non-uuid-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			// First cycle: replaces non-UUID id, writes file, no task published
			s.RunCycle(ctx, results)
			var first scanner.ScanResult
			Expect(results).To(Receive(&first))
			Expect(first.Changed).To(BeEmpty())

			// File on disk now contains a valid UUID
			written, err := os.ReadFile(absPath) // #nosec G304 -- test-only path
			Expect(err).To(BeNil())
			Expect(string(written)).To(ContainSubstring("task_identifier:"))
			Expect(string(written)).NotTo(ContainSubstring("my-custom-id"))

			// Second cycle: publishes with generated UUID
			s.RunCycle(ctx, results)
			var second scanner.ScanResult
			Expect(results).To(Receive(&second))
			Expect(second.Changed).To(HaveLen(1))
			Expect(
				string(second.Changed[0].TaskIdentifier),
			).To(MatchRegexp(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`))
		})

		It("duplicate task_identifier across files is replaced with fresh UUID", func() {
			sharedUUID := "77777777-7777-4777-8777-777777777777"
			content1 := "---\ntask_identifier: " + sharedUUID + "\nstatus: todo\nassignee: claude\n---\n# First file"
			content2 := "---\ntask_identifier: " + sharedUUID + "\nstatus: todo\nassignee: claude\n---\n# Second file (duplicate)"
			absPath1 := filepath.Join(tmpDir, taskDir, "dup-first.md")
			absPath2 := filepath.Join(tmpDir, taskDir, "dup-second.md")
			Expect(os.WriteFile(absPath1, []byte(content1), 0600)).To(Succeed())
			Expect(os.WriteFile(absPath2, []byte(content2), 0600)).To(Succeed())

			// First cycle: first-seen keeps UUID, second gets replaced
			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			// One file published (the first-seen), one replaced (the duplicate)
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].TaskIdentifier)).To(Equal(sharedUUID))

			// Replaced file now has a different UUID on disk
			written, err := os.ReadFile(absPath2) // #nosec G304 -- test-only path
			Expect(err).To(BeNil())
			Expect(string(written)).NotTo(ContainSubstring(sharedUUID))
		})

		It("valid unique UUID task_identifier is preserved unchanged", func() {
			validUUID := "88888888-8888-4888-8888-888888888888"
			content := "---\ntask_identifier: " + validUUID + "\nstatus: todo\nassignee: claude\n---\n# Task with valid UUID"
			absPath := filepath.Join(tmpDir, taskDir, "valid-uuid-task.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			var result scanner.ScanResult
			Expect(results).To(Receive(&result))
			Expect(result.Changed).To(HaveLen(1))
			Expect(string(result.Changed[0].TaskIdentifier)).To(Equal(validUUID))

			// File is unchanged on disk (not rewritten)
			onDisk, err := os.ReadFile(absPath) // #nosec G304 -- test-only path
			Expect(err).To(BeNil())
			Expect(string(onDisk)).To(Equal(content))
		})

		It("CommitAndPush failure suppresses scanner.ScanResult", func() {
			fakeGit.commitPushErr = context.DeadlineExceeded

			content := "---\nstatus: todo\nassignee: claude\n---\n# Task without UUID"
			absPath := filepath.Join(tmpDir, taskDir, "no-uuid-task3.md")
			Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

			s.RunCycle(ctx, results)
			Consistently(results, 50*time.Millisecond).ShouldNot(Receive())
		})
	})

	Describe("SkippedFilesTotal counter", func() {
		counterValue := func(reason string) float64 {
			mfs, err := prometheus.DefaultGatherer.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, mf := range mfs {
				if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
					continue
				}
				for _, m := range mf.GetMetric() {
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "reason" && lp.GetValue() == reason {
							return m.GetCounter().GetValue()
						}
					}
				}
			}
			return 0
		}

		Context("invalid_frontmatter reason (extractFrontmatter failure)", func() {
			// Content without frontmatter delimiter fails at extractFrontmatter, not at DeduplicateFrontmatter.
			// The second cycle re-processes (hash not stored), so counter ticks again.
			It(
				"increments counter on first cycle and again on second cycle (re-scan increments)",
				func() {
					// No frontmatter delimiter - extractFrontmatter fails
					content := "task_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: claude\n---\n# No frontmatter"
					absPath := filepath.Join(tmpDir, taskDir, "no-frontmatter.md")
					Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

					initial := counterValue(metrics.ReasonInvalidFrontmatter)
					initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
					initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
					initialInjectFailed := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
					initialReadFailed := counterValue(metrics.ReasonReadFailed)

					s.RunCycle(ctx, results)
					Expect(counterValue(metrics.ReasonInvalidFrontmatter)).To(Equal(initial + 1))

					s.RunCycle(ctx, results)
					Expect(counterValue(metrics.ReasonInvalidFrontmatter)).To(Equal(initial + 2))

					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(BeNumerically("==", initialDupInvalid))
					Expect(
						counterValue(metrics.ReasonEmptyStatus),
					).To(BeNumerically("==", initialEmptyStatus))
					Expect(
						counterValue(metrics.ReasonInjectTaskIdentifierFailed),
					).To(BeNumerically("==", initialInjectFailed))
					Expect(
						counterValue(metrics.ReasonReadFailed),
					).To(BeNumerically("==", initialReadFailed))
				},
			)
		})

		Context("duplicate_frontmatter_invalid reason", func() {
			// Bare '[' is valid at the raw frontmatter string level (extractFrontmatter passes),
			// but fails DeduplicateFrontmatter's internal unmarshal — the FIRST skip site hit
			// for this content is DeduplicateFrontmatter → duplicate_frontmatter_invalid,
			// NOT extractFrontmatter → invalid_frontmatter.
			// So duplicate_frontmatter_invalid ticks and invalid_frontmatter does NOT tick.
			It(
				"increments duplicate_frontmatter_invalid counter (NOT invalid_frontmatter) on first cycle and again on second cycle",
				func() {
					content := "---\ntask_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: [invalid\n---\n# Invalid"
					absPath := filepath.Join(tmpDir, taskDir, "invalid-yaml-duplicate.md")
					Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

					initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
					initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
					initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
					initialInjectFailed := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
					initialReadFailed := counterValue(metrics.ReasonReadFailed)

					s.RunCycle(ctx, results)
					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(BeNumerically("==", initialDupInvalid+1))
					Expect(
						counterValue(metrics.ReasonInvalidFrontmatter),
					).To(BeNumerically("==", initialInvalid))
					// NOT incremented

					s.RunCycle(ctx, results)
					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(BeNumerically("==", initialDupInvalid+2))
					Expect(
						counterValue(metrics.ReasonInvalidFrontmatter),
					).To(BeNumerically("==", initialInvalid))

					Expect(
						counterValue(metrics.ReasonEmptyStatus),
					).To(BeNumerically("==", initialEmptyStatus))
					Expect(
						counterValue(metrics.ReasonInjectTaskIdentifierFailed),
					).To(BeNumerically("==", initialInjectFailed))
					Expect(
						counterValue(metrics.ReasonReadFailed),
					).To(BeNumerically("==", initialReadFailed))
				},
			)
		})

		Context("empty_status reason", func() {
			// The empty_status check happens AFTER v.hashes[relPath] = fileEntry{hash: hash, ...}
			// is stored, so the second cycle short-circuits at the hash check and
			// the counter does NOT tick again (hash prevents re-process).
			It(
				"increments counter on first cycle but NOT on second cycle (hash prevents re-process)",
				func() {
					content := "---\ntask_identifier: 88888888-8888-4888-8888-888888888888\nassignee: claude\n---\n# Empty status\n"
					absPath := filepath.Join(tmpDir, taskDir, "empty-status.md")
					Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

					initial := counterValue(metrics.ReasonEmptyStatus)
					initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
					initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
					initialInjectFailed := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
					initialReadFailed := counterValue(metrics.ReasonReadFailed)

					s.RunCycle(ctx, results)
					Expect(counterValue(metrics.ReasonEmptyStatus)).To(Equal(initial + 1))

					s.RunCycle(ctx, results)
					Expect(
						counterValue(metrics.ReasonEmptyStatus),
					).To(Equal(initial + 1))
					// NOT incremented again

					Expect(
						counterValue(metrics.ReasonInvalidFrontmatter),
					).To(BeNumerically("==", initialInvalid))
					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(BeNumerically("==", initialDupInvalid))
					Expect(
						counterValue(metrics.ReasonInjectTaskIdentifierFailed),
					).To(BeNumerically("==", initialInjectFailed))
					Expect(
						counterValue(metrics.ReasonReadFailed),
					).To(BeNumerically("==", initialReadFailed))
				},
			)
		})

		Context("read_failed reason", func() {
			// File is listed but ReadFile always fails.
			// Counter increments on each cycle since hash is never stored.
			It("increments counter on first cycle and again on second cycle", func() {
				content := "---\ntask_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: claude\n---\n# Read fail"
				absPath := filepath.Join(tmpDir, taskDir, "read-fail.md")
				Expect(os.WriteFile(absPath, []byte(content), 0600)).To(Succeed())

				initial := counterValue(metrics.ReasonReadFailed)
				initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
				initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
				initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
				initialInjectFailed := counterValue(metrics.ReasonInjectTaskIdentifierFailed)

				failingClient := &failingReadFileOpsGitClient{
					fileOpsTestGitClient: &fileOpsTestGitClient{path: tmpDir},
				}

				vs := scanner.NewGitRestVaultScanner(
					failingClient,
					taskDir,
					time.Hour,
					make(chan struct{}),
					metrics.New(),
				)
				scanResults := make(chan scanner.ScanResult, 1)

				vs.RunCycle(ctx, scanResults)
				Expect(counterValue(metrics.ReasonReadFailed)).To(Equal(initial + 1))

				vs.RunCycle(ctx, scanResults)
				Expect(counterValue(metrics.ReasonReadFailed)).To(Equal(initial + 2))

				Expect(
					counterValue(metrics.ReasonInvalidFrontmatter),
				).To(BeNumerically("==", initialInvalid))
				Expect(
					counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
				).To(BeNumerically("==", initialDupInvalid))
				Expect(
					counterValue(metrics.ReasonEmptyStatus),
				).To(BeNumerically("==", initialEmptyStatus))
				Expect(
					counterValue(metrics.ReasonInjectTaskIdentifierFailed),
				).To(BeNumerically("==", initialInjectFailed))
			})
		})

		Context("broken vs valid regression", func() {
			// Merge marker content passes extractFrontmatter but fails DeduplicateFrontmatter's yaml.Unmarshal.
			// Broken file's hash is never stored in v.hashes, so second cycle re-processes it.
			// Valid file processes normally and is published.
			It(
				"skips broken file, publishes valid file, counter increments again on second cycle for broken file",
				func() {
					brokenContent := "---\n<<<<<<< HEAD\ntask_identifier: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\nstatus: todo\nassignee: claude\n---\n# Broken"
					validContent := "---\ntask_identifier: bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb\nstatus: todo\nassignee: claude\n---\n# Valid"

					brokenPath := filepath.Join(tmpDir, taskDir, "broken-regression.md")
					validPath := filepath.Join(tmpDir, taskDir, "valid-regression.md")
					Expect(os.WriteFile(brokenPath, []byte(brokenContent), 0600)).To(Succeed())
					Expect(os.WriteFile(validPath, []byte(validContent), 0600)).To(Succeed())

					initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
					initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
					initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
					initialInjectFailed := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
					initialReadFailed := counterValue(metrics.ReasonReadFailed)

					s.RunCycle(ctx, results)
					var firstResult scanner.ScanResult
					Expect(results).To(Receive(&firstResult))
					Expect(firstResult.Changed).To(HaveLen(1))
					Expect(
						string(firstResult.Changed[0].TaskIdentifier),
					).To(Equal("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"))
					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(Equal(initialDupInvalid + 1))

					s.RunCycle(ctx, results)
					var secondResult scanner.ScanResult
					Expect(results).To(Receive(&secondResult))
					// valid file unchanged, not re-published
					Expect(secondResult.Changed).To(BeEmpty())
					// broken file re-processed (hash never stored)
					Expect(
						counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
					).To(Equal(initialDupInvalid + 2))

					Expect(
						counterValue(metrics.ReasonInvalidFrontmatter),
					).To(BeNumerically("==", initialInvalid))
					Expect(
						counterValue(metrics.ReasonEmptyStatus),
					).To(BeNumerically("==", initialEmptyStatus))
					Expect(
						counterValue(metrics.ReasonInjectTaskIdentifierFailed),
					).To(BeNumerically("==", initialInjectFailed))
					Expect(
						counterValue(metrics.ReasonReadFailed),
					).To(BeNumerically("==", initialReadFailed))
				},
			)
		})

		It("maintains counter-call parity with skip-site log lines (AC#6 invariant)", func() {
			// vault_scanner.go is in pkg/scanner/ so the test file at pkg/scanner/vault_scanner_test.go
			// finds the source at pkg/scanner/vault_scanner.go (same directory).
			scannerSrc, err := filepath.Abs("vault_scanner.go")
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command(
				"awk",
				`/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/`,
				scannerSrc,
			)
			out, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())
			body := string(out)

			skipCount := strings.Count(body, `glog.Warningf("skipping`) +
				strings.Count(body, `glog.Errorf("skipping`) +
				strings.Count(body, `glog.Warningf("failed to read`)
			counterCount := strings.Count(body, `SkippedFilesTotal(`)
			Expect(skipCount).To(Equal(6), "expected 6 skip-site log lines, got %d", skipCount)
			Expect(
				counterCount,
			).To(Equal(6), "expected 6 counter increment calls, got %d", counterCount)
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

var _ = Describe("domain.NormalizeTaskStatus alias (spec 038)", func() {
	It("normalizes legacy status 'todo' to TaskStatusNext", func() {
		canonical, ok := domain.NormalizeTaskStatus("todo")
		Expect(ok).To(BeTrue())
		Expect(canonical).To(Equal(domain.TaskStatusNext))
	})
})
