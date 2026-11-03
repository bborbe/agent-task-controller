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
