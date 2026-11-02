// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This spec file is internal (package scanner) because these specs drive the
// unexported Run/recomputeOpenTaskCensus paths and read the package gauges. The
// external scanner_test package's doubles are not visible here, so the shared
// hand-written fake in task_age_ticker_internal_test.go is reused.

package scanner

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// openTaskCensusTickerRecord builds an open record with a distinct identifier and
// an explicit phase/task_type, in the shape the scanner parses.
func openTaskCensusTickerRecord(id, phase, taskType string) string {
	return openTaskCensusRecord(
		"task_identifier: "+id,
		"status: in_progress",
		"assignee: claude",
		"phase: "+phase,
		"task_type: "+taskType,
	)
}

// openTaskCensusTickerSentinel reads one census sample directly from the gauge.
// WithLabelValues CREATES a series when absent, so this is only used where the
// series was seeded or is expected to exist.
func openTaskCensusTickerSentinel(vault, phase, taskType string) float64 {
	return testutil.ToFloat64(metrics.New().OpenTasks().WithLabelValues(vault, phase, taskType))
}

var _ = Describe("open task census ticker", func() {
	ctx := context.Background()

	It("publishes on its own ticker under a zero poll interval without scanning (AC 16)", func() {
		const vault = "census-ticker"
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

		// The start-up recompute ran over an empty board and published nothing.
		Consistently(func() bool {
			_, ok := openTaskCensusValue(vault, "planning", "github-update-go")
			return ok
		}, 100*time.Millisecond).Should(BeFalse())

		git.setFile(
			"tasks/open.md",
			openTaskCensusTickerRecord(
				"11111111-1111-4111-8111-111111111111",
				"planning",
				"github-update-go",
			),
		)

		// No trigger fired and the poll interval is zero, so only the metrics tick
		// can have published this.
		Eventually(func() float64 {
			value, _ := openTaskCensusValue(vault, "planning", "github-update-go")
			return value
		}, "2s").Should(Equal(1.0))

		// No scan cycle ran: nothing was published, nothing pulled, nothing written.
		Consistently(results, 300*time.Millisecond).ShouldNot(Receive())
		pullCalls, writeFileCalls := git.counts()
		Expect(pullCalls).To(Equal(0))
		Expect(writeFileCalls).To(Equal(0))

		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})

	It("does not write the census from RunCycle (AC 15)", func() {
		const vault = "census-runcycle"
		const sentinel = 424242.0
		metrics.New().OpenTasks().
			WithLabelValues(vault, "planning", "github-update-go").
			Set(sentinel)

		git := newTaskAgeTickerGitClient()
		git.setFile(
			"tasks/one.md",
			openTaskCensusTickerRecord(
				"11111111-1111-4111-8111-111111111111",
				"planning",
				"github-update-go",
			),
		)
		git.setFile(
			"tasks/two.md",
			openTaskCensusTickerRecord(
				"22222222-2222-4222-8222-222222222222",
				"planning",
				"github-update-go",
			),
		)
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

		// The board would publish 2 at this pair, but the scan path must not write it.
		vs.RunCycle(ctx, make(chan ScanResult, 1))

		Expect(openTaskCensusTickerSentinel(vault, "planning", "github-update-go")).
			To(Equal(sentinel))
	})

	It("does not write the census from Run's start-up index build (AC 15)", func() {
		const vault = "census-build-index"
		const sentinel = 424242.0
		metrics.New().OpenTasks().
			WithLabelValues(vault, "planning", "github-update-go").
			Set(sentinel)

		git := newTaskAgeTickerGitClient()
		git.setFile(
			"tasks/open.md",
			openTaskCensusTickerRecord(
				"11111111-1111-4111-8111-111111111111",
				"planning",
				"github-update-go",
			),
		)
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
			path, _, _ := vs.Resolve(ctx, "11111111-1111-4111-8111-111111111111")
			return path
		}, "2s").Should(Equal("tasks/open.md"))

		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
		Expect(openTaskCensusTickerSentinel(vault, "planning", "github-update-go")).
			To(Equal(sentinel))
	})

	It("writes nothing when the listing fails (AC 17)", func() {
		const vault = "census-list-fail"
		const sentinel = 424242.0
		metrics.New().OpenTasks().
			WithLabelValues(vault, "planning", "github-update-go").
			Set(sentinel)
		before := len(openTaskCensusGathered())

		vs := &vaultScanner{
			metrics:   metrics.New(),
			vaultName: vault,
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return nil, fmt.Errorf("listing failed")
				},
			},
		}
		vs.recomputeOpenTaskCensus(ctx)

		Expect(openTaskCensusTickerSentinel(vault, "planning", "github-update-go")).
			To(Equal(sentinel))
		Expect(openTaskCensusGathered()).To(HaveLen(before))

		// On a fresh vault, a first-pass failure publishes nothing at all.
		const freshVault = "census-list-fail-first"
		fresh := &vaultScanner{
			metrics:   metrics.New(),
			vaultName: freshVault,
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return nil, fmt.Errorf("listing failed")
				},
			},
		}
		fresh.recomputeOpenTaskCensus(ctx)
		_, ok := openTaskCensusValue(freshVault, "planning", "github-update-go")
		Expect(ok).To(BeFalse())
	})

	It("writes nothing when the pass is cancelled mid-way (AC 18)", func() {
		const vault = "census-cancelled"
		const sentinel = 424242.0
		metrics.New().OpenTasks().
			WithLabelValues(vault, "planning", "github-update-go").
			Set(sentinel)

		paths := []string{"a.md", "b.md", "c.md"}
		files := map[string]string{
			"a.md": openTaskCensusTickerRecord(
				"11111111-1111-4111-8111-111111111111",
				"execution",
				"pr-review",
			),
			"b.md": openTaskCensusTickerRecord(
				"22222222-2222-4222-8222-222222222222",
				"planning",
				"other-type",
			),
			"c.md": openTaskCensusTickerRecord(
				"33333333-3333-4333-8333-333333333333",
				"hold",
				"third-type",
			),
		}

		passCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		vs := &vaultScanner{
			metrics:   metrics.New(),
			vaultName: vault,
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return paths, nil
				},
				readFile: func(_ context.Context, relPath string) ([]byte, error) {
					cancel()
					return []byte(files[relPath]), nil
				},
			},
		}
		vs.recomputeOpenTaskCensus(passCtx)

		Expect(openTaskCensusTickerSentinel(vault, "planning", "github-update-go")).
			To(Equal(sentinel))
		for _, pair := range [][2]string{
			{"execution", "pr-review"},
			{"planning", "other-type"},
			{"hold", "third-type"},
		} {
			_, ok := openTaskCensusValue(vault, pair[0], pair[1])
			Expect(ok).To(BeFalse())
		}
	})

	It("is a sibling of the age pass, never folded into it (AC 19)", func() {
		const vault = "census-sibling"
		const sentinel = 424242.0
		metrics.New().OpenTasks().
			WithLabelValues(vault, "planning", "github-update-go").
			Set(sentinel)

		files := map[string]string{
			"a.md": openTaskCensusTickerRecord(
				"11111111-1111-4111-8111-111111111111",
				"planning",
				"github-update-go",
			),
			"b.md": openTaskCensusTickerRecord(
				"22222222-2222-4222-8222-222222222222",
				"planning",
				"github-update-go",
			),
		}
		vs := openTaskCensusScanner(vault, files, nil)

		// The age pass must not publish the census: a folded implementation would
		// overwrite the sentinel.
		vs.recomputeTaskAgeMetrics(ctx)
		Expect(openTaskCensusTickerSentinel(vault, "planning", "github-update-go")).
			To(Equal(sentinel))

		// The census pass must not touch the age series.
		metrics.New().TaskOldestAgeSeconds(vault).Set(sentinel)
		seeded := make(map[metrics.TaskAgeBucket]float64, len(metrics.AvailableTaskAgeBuckets))
		for i, bucket := range metrics.AvailableTaskAgeBuckets {
			value := float64(i + 1)
			metrics.New().TaskAgeBucketTasks(vault, bucket).Set(value)
			seeded[bucket] = value
		}

		vs.recomputeOpenTaskCensus(ctx)

		Expect(taskAgeTickerOldest(vault)).To(Equal(sentinel))
		for bucket, want := range seeded {
			Expect(taskAgeTickerBucket(vault, bucket)).To(Equal(want))
		}
	})
})
