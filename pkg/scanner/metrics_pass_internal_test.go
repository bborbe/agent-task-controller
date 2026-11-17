// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// metricsPassVault isolates this file's series from the other internal tests, which
// share the package-level gauges.
const metricsPassVault = "equivalence-vault"

// metricsPassFiles is a recorded vault state that exercises every classification
// branch: terminal records in both spellings, dated records spanning more than one
// bucket, an undated record, a record with no status, a file whose frontmatter will
// not unmarshal, and (via unreadable) a path that cannot be read at all.
func metricsPassFiles() map[string]string {
	return map[string]string{
		"tasks/recent.md": taskAgeSummaryFile(
			"in_progress", "phase: execution", "task_type: llm",
			taskAgeSummaryQuoted("created", 2*time.Hour),
		),
		"tasks/mid.md": taskAgeSummaryFile(
			"in_progress", "phase: planning", "task_type: dark-factory",
			taskAgeSummaryQuoted("created_date", 3*24*time.Hour),
		),
		"tasks/old.md": taskAgeSummaryFile(
			"in_progress", "phase: human_review", "task_type: llm",
			taskAgeSummaryQuoted("created", 30*24*time.Hour),
		),
		"tasks/undated.md": taskAgeSummaryFile(
			"in_progress", "phase: execution", "task_type: llm",
		),
		"tasks/nostatus.md": taskAgeSummaryFile(
			"", "phase: done", "task_type: llm",
		),
		"tasks/completed.md": taskAgeSummaryFile(
			"completed", "phase: done", "task_type: llm",
			taskAgeSummaryQuoted("created", 10*24*time.Hour),
		),
		"tasks/aborted.md": taskAgeSummaryFile(
			"aborted", "phase: done", "task_type: llm",
			taskAgeSummaryQuoted("created", 40*24*time.Hour),
		),
		"tasks/broken.md": "---\nstatus: in_progress\ncreated: [unclosed\n---\n",
	}
}

// metricsPassUnreadable is the path the fake readFile fails on.
func metricsPassUnreadable() map[string]struct{} {
	return map[string]struct{}{"tasks/gone.md": {}}
}

// metricsPassScanner builds a scanner over a fixed vault state. reads, when non-nil,
// counts readFile calls so a test can assert how many times the vault was fetched.
func metricsPassScanner(
	files map[string]string,
	unreadable map[string]struct{},
	reads *int,
) *vaultScanner {
	paths := make([]string, 0, len(files)+len(unreadable))
	for relPath := range files {
		paths = append(paths, relPath)
	}
	for relPath := range unreadable {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)

	return &vaultScanner{
		metrics:   metrics.New(),
		taskDir:   "tasks",
		vaultName: metricsPassVault,
		ops: fileOps{
			listFiles: func(_ context.Context, _ string) ([]string, error) {
				return paths, nil
			},
			readFile: func(_ context.Context, relPath string) ([]byte, error) {
				if reads != nil {
					*reads = *reads + 1
				}
				if _, ok := unreadable[relPath]; ok {
					return nil, os.ErrNotExist
				}
				content, ok := files[relPath]
				if !ok {
					return nil, os.ErrNotExist
				}
				return []byte(content), nil
			},
		},
	}
}

// metricsPassSnapshot reads the three instruments' series straight off their
// collectors, keyed by instrument and sorted labels, so two runs can be compared
// as maps. Reading the collectors rather than the registry keeps the snapshot
// independent of which other specs have registered or reset a series.
func metricsPassSnapshot() map[string]float64 {
	instruments := []struct {
		name      string
		collector prometheus.Collector
	}{
		{"oldest", metrics.TaskOldestAgeSeconds},
		{"buckets", metrics.TaskAgeBucketTasks},
		{"census", metrics.New().OpenTasks()},
	}

	snapshot := make(map[string]float64)
	for _, instrument := range instruments {
		ch := make(chan prometheus.Metric, 256)
		go func(collector prometheus.Collector) {
			collector.Collect(ch)
			close(ch)
		}(instrument.collector)
		for metric := range ch {
			written := &dto.Metric{}
			if err := metric.Write(written); err != nil {
				continue
			}
			labels := make([]string, 0, len(written.GetLabel()))
			for _, label := range written.GetLabel() {
				labels = append(labels, label.GetName()+"="+label.GetValue())
			}
			sort.Strings(labels)
			snapshot[instrument.name+"{"+strings.Join(labels, ",")+"}"] = written.GetGauge().
				GetValue()
		}
	}
	return snapshot
}

// parkedProbeGaugeValue reads the parked-probe gauge straight off its collector, so
// the spec does not depend on which other specs have registered or reset a series.
func parkedProbeGaugeValue() float64 {
	ch := make(chan prometheus.Metric, 1)
	go func() {
		metrics.HealthcheckParkedProbes.Collect(ch)
		close(ch)
	}()
	for metric := range ch {
		written := &dto.Metric{}
		Expect(metric.Write(written)).To(Succeed())
		return written.GetGauge().GetValue()
	}
	return -1
}

// updateGoStuckGaugeValue reads the update-go stuck-planning gauge straight off its
// collector, so the spec does not depend on which other specs have registered or reset
// a series.
func updateGoStuckGaugeValue() float64 {
	ch := make(chan prometheus.Metric, 1)
	go func() {
		metrics.UpdateGoStuckPlanningTasks.Collect(ch)
		close(ch)
	}()
	for metric := range ch {
		written := &dto.Metric{}
		Expect(metric.Write(written)).To(Succeed())
		return written.GetGauge().GetValue()
	}
	return -1
}

var _ = Describe("recomputeVaultMetrics", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		metrics.New().OpenTasks().Reset()
	})

	It("publishes exactly what the two independent passes published", func() {
		files := metricsPassFiles()
		unreadable := metricsPassUnreadable()

		// The pre-change behaviour: two independent passes over the same vault.
		legacy := metricsPassScanner(files, unreadable, nil)
		legacy.recomputeTaskAgeMetrics(ctx)
		legacy.recomputeOpenTaskCensus(ctx)
		want := metricsPassSnapshot()

		metrics.New().OpenTasks().Reset()

		// The new behaviour: one shared pass.
		merged := metricsPassScanner(files, unreadable, nil)
		merged.recomputeVaultMetrics(ctx)
		got := metricsPassSnapshot()

		// The oldest age is measured against time.Now() inside each pass, so two runs
		// that straddle a second boundary legitimately differ by up to a second.
		// Every bucket and every census pair must match exactly.
		oldestKey := `oldest{vault=` + metricsPassVault + `}`
		wantOldest, wantHas := want[oldestKey]
		gotOldest, gotHas := got[oldestKey]
		Expect(wantHas).To(BeTrue())
		Expect(gotHas).To(BeTrue())
		delete(want, oldestKey)
		delete(got, oldestKey)

		Expect(got).To(Equal(want))
		Expect(gotOldest).To(BeNumerically("~", wantOldest, 2))
		Expect(gotOldest).To(BeNumerically(">", 0))
	})

	It("sets the parked-probe gauge from the same pass", func() {
		// This pass is the gauge's writer, not the scan cycle: every controller on dev
		// and prod runs POLL_INTERVAL=0s, where scanFiles is dead, so a gauge set there
		// would keep its init value forever and a parked probe would stay invisible.
		// The four records are the acceptance criterion's matrix — parked, running,
		// terminal, and one whose frontmatter will not parse.
		v := metricsPassScanner(map[string]string{
			"tasks/probe-parked.md": "---\ntask_type: healthcheck\nphase: human_review\n" +
				"status: hold\ncurrent_job: \"\"\n---\n\nreply 'ok'\n",
			"tasks/probe-running.md": "---\ntask_type: healthcheck\nphase: planning\n" +
				"status: in_progress\ncurrent_job: \"job-1\"\n---\n\nreply 'ok'\n",
			"tasks/probe-terminal.md": "---\ntask_type: healthcheck\nphase: done\n" +
				"status: completed\ncurrent_job: \"\"\n---\n\nreply 'ok'\n",
			"tasks/broken.md": "---\nstatus: [unclosed\n---\n",
		}, nil, nil)

		_, _, ok := v.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(parkedProbeGaugeValue()).To(BeNumerically("==", 1))
	})

	It("leaves the parked-probe gauge untouched when the listing fails", func() {
		// A failed listing writes nothing, exactly as the age and census series behave:
		// publishing 0 for a pass that never ran would blank a real signal.
		v := metricsPassScanner(map[string]string{}, nil, nil)
		v.ops.listFiles = func(_ context.Context, _ string) ([]string, error) {
			return nil, os.ErrNotExist
		}
		metrics.HealthcheckParkedProbes.Set(7)

		_, _, ok := v.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeFalse())
		Expect(parkedProbeGaugeValue()).To(BeNumerically("==", 7))
	})

	It("sets the update-go stuck gauge from the same pass", func() {
		// This pass is the gauge's writer, not the scan cycle: every controller on dev
		// and prod runs POLL_INTERVAL=0s, where scanFiles is dead, so a gauge set there
		// kept its init value for the pod's whole life and the stuck-record signal was
		// dead everywhere. The four records are the acceptance criterion's matrix —
		// stuck, too young, past planning, and one whose frontmatter will not parse.
		oldStart := time.Now().Add(-25 * time.Hour).Format(time.RFC3339)
		recentStart := time.Now().Add(-time.Hour).Format(time.RFC3339)
		failureBody := "\n# Update Go\n\n## Failure\n\n- **Reason:** deadline_exceeded\n"
		updateGo := func(phase, startedAt string) string {
			return "---\ntask_type: github-update-go\nphase: " + phase +
				"\nstatus: in_progress\njob_started_at: \"" + startedAt + "\"\n---\n" +
				failureBody
		}

		v := metricsPassScanner(map[string]string{
			"tasks/Update Go bborbe-alert a34dc9e.md":     updateGo("planning", oldStart),
			"tasks/Update Go bborbe-run 1111111.md":       updateGo("planning", recentStart),
			"tasks/Update Go bborbe-vault-cli 2222222.md": updateGo("human_review", oldStart),
			"tasks/broken.md":                             "---\nstatus: [unclosed\n---\n",
		}, nil, nil)

		_, _, ok := v.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(updateGoStuckGaugeValue()).To(BeNumerically("==", 1))
	})

	It("leaves the update-go stuck gauge untouched when the listing fails", func() {
		// A failed listing writes nothing, exactly as the age, census and parked-probe
		// series behave: publishing 0 for a pass that never ran would blank a real
		// signal and read as "the backlog drained".
		v := metricsPassScanner(map[string]string{}, nil, nil)
		v.ops.listFiles = func(_ context.Context, _ string) ([]string, error) {
			return nil, os.ErrNotExist
		}
		metrics.UpdateGoStuckPlanningTasks.Set(3)

		_, _, ok := v.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeFalse())
		Expect(updateGoStuckGaugeValue()).To(BeNumerically("==", 3))
	})

	It("reads each task file once where the two passes read it twice", func() {
		files := metricsPassFiles()
		unreadable := metricsPassUnreadable()
		total := len(files) + len(unreadable)

		legacyReads := 0
		legacy := metricsPassScanner(files, unreadable, &legacyReads)
		legacy.recomputeTaskAgeMetrics(ctx)
		legacy.recomputeOpenTaskCensus(ctx)
		Expect(legacyReads).To(Equal(2 * total))

		mergedReads := 0
		merged := metricsPassScanner(files, unreadable, &mergedReads)
		merged.recomputeVaultMetrics(ctx)
		Expect(mergedReads).To(Equal(total))
	})

	It("writes nothing when the listing fails", func() {
		metrics.New().OpenTasks().Reset()

		seeded := metricsPassScanner(metricsPassFiles(), metricsPassUnreadable(), nil)
		seeded.recomputeVaultMetrics(ctx)
		before := metricsPassSnapshot()

		failing := &vaultScanner{
			metrics:   metrics.New(),
			taskDir:   "tasks",
			vaultName: metricsPassVault,
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return nil, os.ErrPermission
				},
			},
		}
		failing.recomputeVaultMetrics(ctx)

		Expect(metricsPassSnapshot()).To(Equal(before))
	})
})
