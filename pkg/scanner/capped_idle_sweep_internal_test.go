// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// This spec file is internal (package scanner) because the sweep is driven through
// the unexported Run/recomputeVaultMetrics paths. The external scanner_test
// package's doubles are not visible here, so the fake below is hand-written.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// sweepGitClient is a mutex-guarded gitclient.GitClient over a real temp dir. The
// scanner goroutine and the spec goroutine both touch it, so every mutable field is
// read and written under mu. It embeds the interface for the methods these specs
// never exercise.
type sweepGitClient struct {
	gitclient.GitClient

	dir string

	mu            sync.Mutex
	pullCalls     int
	readFileCalls int
}

var _ gitclient.GitClient = (*sweepGitClient)(nil)

func newSweepGitClient(dir string) *sweepGitClient {
	return &sweepGitClient{dir: dir}
}

func (c *sweepGitClient) Path() string { return c.dir }

func (c *sweepGitClient) Pull(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pullCalls++
	return nil
}

func (c *sweepGitClient) CommitAndPush(_ context.Context, _ string) error { return nil }

func (c *sweepGitClient) ListFiles(_ context.Context, glob string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(c.dir, glob))
	if err != nil {
		return nil, err
	}
	rel := make([]string, 0, len(matches))
	for _, m := range matches {
		r, relErr := filepath.Rel(c.dir, m)
		if relErr != nil {
			return nil, relErr
		}
		rel = append(rel, r)
	}
	return rel, nil
}

func (c *sweepGitClient) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	c.mu.Lock()
	c.readFileCalls++
	c.mu.Unlock()
	return os.ReadFile(filepath.Join(c.dir, relPath)) // #nosec G304 -- test-only path
}

func (c *sweepGitClient) WriteFile(_ context.Context, relPath string, content []byte) error {
	return os.WriteFile(
		filepath.Join(c.dir, relPath),
		content,
		0600,
	) // #nosec G306 -- test-only file
}

// AtomicReadModifyWriteAndCommitPush performs the real read-modify-write against the
// temp dir. The embedded interface's version would panic, and a no-op stub would make
// the escalation specs pass against a write that never happened.
func (c *sweepGitClient) AtomicReadModifyWriteAndCommitPush(
	_ context.Context,
	absPath string,
	modify func([]byte) ([]byte, error),
	_ string,
) error {
	current, err := os.ReadFile(absPath) // #nosec G304 -- test-only path
	if err != nil {
		return err
	}
	next, err := modify(current)
	if err != nil {
		return err
	}
	if string(next) == string(current) {
		return nil
	}
	return os.WriteFile(absPath, next, 0600) // #nosec G306 -- test-only file
}

func (c *sweepGitClient) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readFileCalls
}

func (c *sweepGitClient) pulls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pullCalls
}

// sweepRecord renders a task file with the given frontmatter lines and a plain body.
func sweepRecord(fmLines ...string) string {
	doc := "---\n"
	for _, line := range fmLines {
		doc += line + "\n"
	}
	doc += "---\n\nBody prose.\n"
	return doc
}

// sweepCappedRecord is a record at its default trigger cap of 3, still assigned.
func sweepCappedRecord() string {
	return sweepRecord(
		"task_identifier: 55555555-5555-4555-8555-555555555555",
		"status: in_progress",
		"phase: planning",
		"assignee: pr-reviewer-agent",
		"ref: \"0000000000000000000000000000000000000000\"",
		"trigger_count: 3",
	)
}

var _ = Describe("periodic capped-idle sweep", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// startScanner builds a trigger-only scanner (POLL_INTERVAL=0s, what prod runs)
	// whose metrics ticker — and therefore the sweep — fires every 25 ms.
	startScanner := func(dir string, metricsInterval time.Duration) (*sweepGitClient, func()) {
		git := newSweepGitClient(dir)
		vs := NewGitRestVaultScanner(
			git,
			"tasks",
			0,
			make(chan struct{}),
			metrics.New(),
			true,
			"test-vault",
			metricsInterval,
		)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- vs.Run(runCtx, make(chan ScanResult, 1))
		}()
		return git, func() {
			cancel()
			Eventually(done, "2s").Should(Receive(BeNil()))
		}
	}

	writeTask := func(dir, name, content string) {
		Expect(os.MkdirAll(filepath.Join(dir, "tasks"), 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "tasks", name), []byte(content), 0600)).
			To(Succeed())
	}

	readTask := func(dir, name string) string {
		content, err := os.ReadFile(
			filepath.Join(dir, "tasks", name),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		return string(content)
	}

	// This is the defect the task exists to remove: a record that did not exist when
	// the controller booted reached no sweep at all, because buildIndex collects its
	// candidates into a slice that dies with the function and v.hashes is never
	// populated on a trigger-only controller.
	It("escalates a record created after the controller has been running", func() {
		dir := GinkgoT().TempDir()
		// A record present at boot, below its cap: it must not be escalated, and its
		// presence proves the boot pass had something to look at.
		writeTask(dir, "below-cap.md", sweepRecord(
			"task_identifier: 66666666-6666-4666-8666-666666666666",
			"status: in_progress",
			"phase: planning",
			"assignee: pr-reviewer-agent",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 2",
		))

		git, stop := startScanner(dir, 25*time.Millisecond)
		defer stop()

		// Wait until at least two full passes have run, so the record written next is
		// unambiguously created after the controller started.
		Eventually(git.reads, "2s").Should(BeNumerically(">=", 2))

		writeTask(dir, "created-later.md", sweepCappedRecord())

		Eventually(func() string { return readTask(dir, "created-later.md") }, "2s").
			Should(And(
				ContainSubstring("status: hold"),
				ContainSubstring("## Trigger Cap Escalation"),
				ContainSubstring("previous_assignee: pr-reviewer-agent"),
			))

		// The sweep is not the scan cycle: no pull ran, so nothing but the recurring
		// metrics pass could have escalated this file.
		Expect(git.pulls()).To(Equal(0))
	})

	It("leaves below-cap, terminal and live-current_job records untouched", func() {
		dir := GinkgoT().TempDir()
		belowCap := sweepRecord(
			"task_identifier: 77777777-7777-4777-8777-777777777777",
			"status: in_progress",
			"phase: planning",
			"assignee: pr-reviewer-agent",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 2",
		)
		terminal := sweepRecord(
			"task_identifier: 88888888-8888-4888-8888-888888888888",
			"status: completed",
			"phase: done",
			"assignee: pr-reviewer-agent",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)
		liveJob := sweepRecord(
			"task_identifier: 99999999-9999-4999-8999-999999999999",
			"status: in_progress",
			"phase: execution",
			"assignee: pr-reviewer-agent",
			"current_job: job-123",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)
		writeTask(dir, "below-cap.md", belowCap)
		writeTask(dir, "terminal.md", terminal)
		writeTask(dir, "live-job.md", liveJob)

		git, stop := startScanner(dir, 25*time.Millisecond)
		defer stop()

		// Several passes must run before the absence means anything.
		Eventually(git.reads, "2s").Should(BeNumerically(">=", 9))

		Expect(readTask(dir, "below-cap.md")).To(Equal(belowCap))
		Expect(readTask(dir, "terminal.md")).To(Equal(terminal))
		Expect(readTask(dir, "live-job.md")).To(Equal(liveJob))
	})

	It("rebuilds its candidate set from the pass it just read", func() {
		dir := GinkgoT().TempDir()
		writeTask(dir, "capped.md", sweepCappedRecord())
		writeTask(dir, "below-cap.md", sweepRecord(
			"task_identifier: 66666666-6666-4666-8666-666666666666",
			"status: in_progress",
			"assignee: pr-reviewer-agent",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 1",
		))

		vs, ok := NewGitRestVaultScanner(
			newSweepGitClient(dir), "tasks", 0, make(chan struct{}),
			metrics.New(), true, "test-vault", 0,
		).(*vaultScanner)
		Expect(ok).To(BeTrue())

		candidates, fileCount := vs.recomputeVaultMetrics(ctx)
		Expect(fileCount).To(Equal(2))
		Expect(candidates).To(ConsistOf("tasks/capped.md"))
	})
})
