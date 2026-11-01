// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// This spec file is internal (package scanner) because the ticker and single-writer
// specs drive the unexported Run/recomputeTaskAgeMetrics paths and seed the package
// gauges through the metrics accessor. The external scanner_test package's doubles
// are not visible here, so the fake below is hand-written.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// taskAgeTickerGitClient is a mutex-guarded in-memory gitclient.GitClient. The
// scanner goroutine and the spec goroutine both touch it, so every mutable field is
// read and written under mu. It embeds the interface for the methods these specs
// never exercise.
type taskAgeTickerGitClient struct {
	gitclient.GitClient

	mu             sync.Mutex
	files          map[string]string
	pullCalls      int
	writeFileCalls int
}

var _ gitclient.GitClient = (*taskAgeTickerGitClient)(nil)

func newTaskAgeTickerGitClient() *taskAgeTickerGitClient {
	return &taskAgeTickerGitClient{files: make(map[string]string)}
}

func (c *taskAgeTickerGitClient) Path() string { return "." }

func (c *taskAgeTickerGitClient) Pull(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pullCalls++
	return nil
}

func (c *taskAgeTickerGitClient) CommitAndPush(_ context.Context, _ string) error { return nil }

func (c *taskAgeTickerGitClient) ListFiles(
	_ context.Context,
	glob string,
) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make([]string, 0, len(c.files))
	for relPath := range c.files {
		matched, err := filepath.Match(glob, relPath)
		if err != nil {
			return nil, err
		}
		if matched {
			paths = append(paths, relPath)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (c *taskAgeTickerGitClient) ReadFile(
	_ context.Context,
	relPath string,
) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	content, ok := c.files[relPath]
	if !ok {
		return nil, fmt.Errorf("no such file: %s", relPath)
	}
	return []byte(content), nil
}

func (c *taskAgeTickerGitClient) WriteFile(
	_ context.Context,
	relPath string,
	content []byte,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeFileCalls++
	c.files[relPath] = string(content)
	return nil
}

func (c *taskAgeTickerGitClient) setFile(relPath, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[relPath] = content
}

func (c *taskAgeTickerGitClient) counts() (pullCalls, writeFileCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pullCalls, c.writeFileCalls
}

func taskAgeTickerOldest(vault string) float64 {
	return testutil.ToFloat64(metrics.New().TaskOldestAgeSeconds(vault))
}

func taskAgeTickerBucket(vault string, bucket metrics.TaskAgeBucket) float64 {
	return testutil.ToFloat64(metrics.New().TaskAgeBucketTasks(vault, bucket))
}

// taskAgeTickerRecord builds a non-terminal record whose created timestamp is
// age before now, in the form the age reader parses.
func taskAgeTickerRecord(age time.Duration) string {
	created := time.Now().Add(-age).UTC().Format(time.RFC3339)
	return "---\n" +
		"task_identifier: 99999999-9999-4999-8999-999999999999\n" +
		"status: in_progress\n" +
		"assignee: claude\n" +
		"created: \"" + created + "\"\n" +
		"---\n# Task\n"
}

var _ = Describe("task age metrics ticker", func() {
	ctx := context.Background()

	It("publishes on its own ticker under a zero poll interval without scanning", func() {
		const vault = "openclaw"
		// Reset the shared series so "empty board reads 0" does not depend on spec order.
		metrics.New().TaskOldestAgeSeconds(vault).Set(0)
		Expect(taskAgeTickerOldest(vault)).To(Equal(0.0))

		git := newTaskAgeTickerGitClient()
		vs := NewGitRestVaultScanner(
			git,
			"tasks",
			0,
			make(chan struct{}),
			metrics.New(),
			true,
			vault,
			25*time.Millisecond,
		)

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		results := make(chan ScanResult, 1)
		done := make(chan error, 1)
		go func() {
			done <- vs.Run(runCtx, results)
		}()

		// The start-up recompute publishes the empty board immediately.
		Eventually(func() float64 { return taskAgeTickerOldest(vault) }, "2s").
			Should(Equal(0.0))

		git.setFile("tasks/aged.md", taskAgeTickerRecord(10*time.Minute))

		Eventually(func() float64 { return taskAgeTickerOldest(vault) }, "2s").
			Should(BeNumerically(">", 0))

		// No scan cycle ran: nothing was published, nothing pulled, nothing written.
		Consistently(results, 300*time.Millisecond).ShouldNot(Receive())
		pullCalls, writeFileCalls := git.counts()
		Expect(pullCalls).To(Equal(0))
		Expect(writeFileCalls).To(Equal(0))

		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})

	It("labels the series with the constructor's vault name verbatim", func() {
		const vault = "openclaw"
		git := newTaskAgeTickerGitClient()
		git.setFile("tasks/aged.md", taskAgeTickerRecord(2*time.Hour))

		vs := NewGitRestVaultScanner(
			git,
			"tasks",
			0,
			make(chan struct{}),
			metrics.New(),
			true,
			vault,
			0,
		)
		internal, ok := vs.(*vaultScanner)
		Expect(ok).To(BeTrue())
		internal.recomputeTaskAgeMetrics(ctx)

		Expect(taskAgeTickerOldest(vault)).To(BeNumerically(">", 0))
		Expect(taskAgeTickerBucket(vault, metrics.TaskAgeBucketLt1d)).To(Equal(1.0))
		// No display-name/slug mapping: the vault-api display name carries nothing.
		Expect(taskAgeTickerOldest("private-agent")).To(Equal(0.0))
	})

	It("does not write the age series from RunCycle", func() {
		const vault = "openclaw"
		const sentinel = 424242.0
		metrics.New().TaskOldestAgeSeconds(vault).Set(sentinel)

		git := newTaskAgeTickerGitClient()
		git.setFile("tasks/aged.md", taskAgeTickerRecord(10*24*time.Hour))
		vs := NewGitRestVaultScanner(
			git,
			"tasks",
			0,
			make(chan struct{}),
			metrics.New(),
			true,
			vault,
			0,
		)
		vs.RunCycle(ctx, make(chan ScanResult, 1))

		Expect(taskAgeTickerOldest(vault)).To(Equal(sentinel))
	})

	It("does not write the age series from Run's start-up index build", func() {
		const vault = "openclaw"
		const sentinel = 424242.0
		metrics.New().TaskOldestAgeSeconds(vault).Set(sentinel)

		git := newTaskAgeTickerGitClient()
		git.setFile("tasks/aged.md", taskAgeTickerRecord(10*24*time.Hour))
		vs := NewGitRestVaultScanner(
			git,
			"tasks",
			0,
			make(chan struct{}),
			metrics.New(),
			true,
			vault,
			0,
		)

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- vs.Run(runCtx, make(chan ScanResult, 1))
		}()

		// buildIndex ran once the seeded record resolves.
		Eventually(func() string {
			path, _, _ := vs.Resolve(ctx, "99999999-9999-4999-8999-999999999999")
			return path
		}, "2s").Should(Equal("tasks/aged.md"))

		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
		Expect(taskAgeTickerOldest(vault)).To(Equal(sentinel))
	})

	It("leaves every published value untouched when the listing fails", func() {
		const vault = "openclaw"
		const sentinel = 424242.0
		metrics.New().TaskOldestAgeSeconds(vault).Set(sentinel)
		seeded := make(map[metrics.TaskAgeBucket]float64, len(metrics.AvailableTaskAgeBuckets))
		for i, bucket := range metrics.AvailableTaskAgeBuckets {
			value := float64(i + 1)
			metrics.New().TaskAgeBucketTasks(vault, bucket).Set(value)
			seeded[bucket] = value
		}

		vs := &vaultScanner{
			metrics:   metrics.New(),
			vaultName: vault,
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return nil, fmt.Errorf("listing failed")
				},
			},
		}
		vs.recomputeTaskAgeMetrics(ctx)

		Expect(taskAgeTickerOldest(vault)).To(Equal(sentinel))
		for bucket, want := range seeded {
			Expect(taskAgeTickerBucket(vault, bucket)).To(Equal(want))
		}
	})
})
