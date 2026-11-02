// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

func taskAgeSummaryNow() time.Time {
	return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
}

func taskAgeSummaryScanner(files map[string]string, unreadable map[string]struct{}) *vaultScanner {
	return &vaultScanner{
		metrics: metrics.New(),
		ops: fileOps{
			readFile: func(_ context.Context, relPath string) ([]byte, error) {
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

func taskAgeSummaryFile(status string, fields ...string) string {
	content := "---\nstatus: " + status + "\n"
	for _, field := range fields {
		content += field + "\n"
	}
	return content + "---\n# Task\n"
}

func taskAgeSummaryQuoted(name string, d time.Duration) string {
	return name + `: "` + taskAgeSummaryNow().Add(-d).Format(time.RFC3339) + `"`
}

func taskAgeSummaryOldest(vault string) float64 {
	return testutil.ToFloat64(metrics.New().TaskOldestAgeSeconds(vault))
}

func taskAgeSummaryBucket(vault string, bucket metrics.TaskAgeBucket) float64 {
	return testutil.ToFloat64(metrics.New().TaskAgeBucketTasks(vault, bucket))
}

func taskAgeSummaryBucketSum(vault string) float64 {
	sum := 0.0
	for _, bucket := range metrics.AvailableTaskAgeBuckets {
		sum += taskAgeSummaryBucket(vault, bucket)
	}
	return sum
}

func taskAgeSummarySnapshot(vault string) map[string]float64 {
	snapshot := map[string]float64{"oldest": taskAgeSummaryOldest(vault)}
	for _, bucket := range metrics.AvailableTaskAgeBuckets {
		snapshot[bucket.String()] = taskAgeSummaryBucket(vault, bucket)
	}
	return snapshot
}

// taskAgeSummaryGatheredBuckets locates the bucket family without naming it: it is
// the only gathered family whose samples carry a bucket label. It returns the
// bucket label value to gauge value map for the given vault.
func taskAgeSummaryGatheredBuckets(vault string) map[string]float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		samples := mf.GetMetric()
		if len(samples) == 0 {
			continue
		}
		hasBucket := false
		for _, lp := range samples[0].GetLabel() {
			if lp.GetName() == "bucket" {
				hasBucket = true
			}
		}
		if !hasBucket {
			continue
		}
		result := make(map[string]float64, len(samples))
		for _, sample := range samples {
			labels := make(map[string]string, len(sample.GetLabel()))
			for _, lp := range sample.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["vault"] != vault {
				continue
			}
			result[labels["bucket"]] = sample.GetGauge().GetValue()
		}
		return result
	}
	Fail("no metric family carrying a bucket label found")
	return nil
}

func taskAgeSummaryExpectAllBucketsPresent(vault string, want float64) {
	gathered := taskAgeSummaryGatheredBuckets(vault)
	for _, bucket := range metrics.AvailableTaskAgeBuckets {
		Expect(gathered).To(HaveKey(bucket.String()))
		Expect(gathered[bucket.String()]).To(Equal(want))
	}
}

var _ = Describe("publishTaskAgeMetrics", func() {
	ctx := context.Background()

	It("classifies records at the exact bucket boundaries", func() {
		const vault = "test-boundaries"
		files := map[string]string{
			"lt.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", taskAgeOneDay-time.Second),
			),
			"one-day.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", taskAgeOneDay),
			),
			"seven.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", taskAgeSevenDays),
			),
			"gt-seven.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", taskAgeSevenDays+time.Second),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{"lt.md", "one-day.md", "seven.md", "gt-seven.md"},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketLt1d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(2.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketGt7d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})

	It("reads any of the four timestamp fields with the same age", func() {
		const vault = "test-precedence"
		at := taskAgeSummaryNow().Add(-48 * time.Hour).Format(time.RFC3339)
		files := map[string]string{
			"created.md":      taskAgeSummaryFile("in_progress", `created: "`+at+`"`),
			"created-date.md": taskAgeSummaryFile("in_progress", `created_date: "`+at+`"`),
			"queued-at.md":    taskAgeSummaryFile("in_progress", `queued_at: "`+at+`"`),
			"job-started.md":  taskAgeSummaryFile("in_progress", `job_started_at: "`+at+`"`),
			"fallback.md": taskAgeSummaryFile(
				"in_progress",
				`created: "not-a-timestamp"`,
				`created_date: "`+at+`"`,
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{
				"created.md",
				"created-date.md",
				"queued-at.md",
				"job-started.md",
				"fallback.md",
			},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(172800.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(5.0))
	})

	It("counts records with no timestamp in undated", func() {
		const vault = "test-undated"
		files := map[string]string{
			"a.md": taskAgeSummaryFile("in_progress"),
			"b.md": taskAgeSummaryFile("pending"),
			"c.md": taskAgeSummaryFile(""),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(ctx, []string{"a.md", "b.md", "c.md"}, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(3.0))
	})

	It("excludes terminal records from every bucket", func() {
		const vault = "test-terminal"
		files := map[string]string{
			"completed.md": taskAgeSummaryFile(
				"completed",
				taskAgeSummaryQuoted("created", time.Hour),
			),
			"aborted.md": taskAgeSummaryFile(
				"aborted",
				taskAgeSummaryQuoted("created", time.Hour),
			),
			"in-progress.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 48*time.Hour),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{"completed.md", "aborted.md", "in-progress.md"},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketLt1d)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketGt7d)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})

	It("publishes every bucket series even over an empty path list", func() {
		const vault = "test-presence"
		v := taskAgeSummaryScanner(map[string]string{}, nil)
		v.publishTaskAgeMetrics(ctx, nil, taskAgeSummaryNow(), vault)
		taskAgeSummaryExpectAllBucketsPresent(vault, 0)
	})

	It("resets the oldest age when the record turns terminal", func() {
		const vault = "test-reset"
		files := map[string]string{
			"task.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 72*time.Hour),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		paths := []string{"task.md"}
		v.publishTaskAgeMetrics(ctx, paths, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(259200.0))

		files["task.md"] = taskAgeSummaryFile(
			"completed",
			taskAgeSummaryQuoted("created", 72*time.Hour),
		)
		v.publishTaskAgeMetrics(ctx, paths, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(0.0))
		taskAgeSummaryExpectAllBucketsPresent(vault, 0)
	})

	It("reports the maximum age, not a sum or an average", func() {
		const vault = "test-maximum"
		files := map[string]string{
			"hour.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", time.Hour),
			),
			"three.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 3*24*time.Hour),
			),
			"ten.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 10*24*time.Hour),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{"hour.md", "three.md", "ten.md"},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(864000.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketLt1d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketGt7d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})

	It("publishes the same values on repeated passes", func() {
		const vault = "test-idempotency"
		files := map[string]string{
			"task.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 2*time.Hour),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		paths := []string{"task.md"}
		v.publishTaskAgeMetrics(ctx, paths, taskAgeSummaryNow(), vault)
		first := taskAgeSummarySnapshot(vault)
		Expect(first["oldest"]).To(Equal(7200.0))

		v.publishTaskAgeMetrics(ctx, paths, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummarySnapshot(vault)).To(Equal(first))
	})

	It("skips unreadable, frontmatter-less and malformed files", func() {
		const vault = "test-skipped"
		files := map[string]string{
			"old.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", 10*24*time.Hour),
			),
			"no-fm.md":     "# just a body\n",
			"malformed.md": "---\nstatus: [unclosed\n---\n# body\n",
		}
		unreadable := map[string]struct{}{"unreadable.md": {}}
		v := taskAgeSummaryScanner(files, unreadable)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{"old.md", "unreadable.md", "no-fm.md", "malformed.md"},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(864000.0))
		Expect(taskAgeSummaryBucketSum(vault)).To(Equal(1.0))
	})

	It("counts a duplicate-key record once, last value winning", func() {
		const vault = "test-duplicate"
		content := "---\n" +
			"status: completed\n" +
			"created: \"2026-10-01T12:00:00Z\"\n" +
			"status: in_progress\n" +
			"created: \"2026-10-29T12:00:00Z\"\n" +
			"---\n# Task\n"
		v := taskAgeSummaryScanner(map[string]string{"dup.md": content}, nil)
		v.publishTaskAgeMetrics(ctx, []string{"dup.md"}, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(172800.0))
		Expect(taskAgeSummaryBucketSum(vault)).To(Equal(1.0))
	})

	It("clamps a future timestamp to zero age", func() {
		const vault = "test-future"
		files := map[string]string{
			"future.md": taskAgeSummaryFile(
				"in_progress",
				taskAgeSummaryQuoted("created", -time.Hour),
			),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(ctx, []string{"future.md"}, taskAgeSummaryNow(), vault)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketLt1d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketGt7d)).To(Equal(0.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})

	It("ages an unquoted RFC3339 timestamp exactly like its quoted twin", func() {
		const vault = "test-unquoted-rfc3339"
		files := map[string]string{
			"unquoted.md": taskAgeSummaryFile("in_progress", "created: 2026-10-29T12:00:00Z"),
			"quoted.md":   taskAgeSummaryFile("in_progress", `created: "2026-10-29T12:00:00Z"`),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(
			ctx,
			[]string{"unquoted.md", "quoted.md"},
			taskAgeSummaryNow(),
			vault,
		)
		Expect(taskAgeSummaryOldest(vault)).To(Equal(172800.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(2.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})

	It("ages an unquoted date-only timestamp from midnight UTC", func() {
		const vault = "test-unquoted-dateonly"
		files := map[string]string{
			"date-only.md": taskAgeSummaryFile("in_progress", "created: 2026-10-29"),
		}
		v := taskAgeSummaryScanner(files, nil)
		v.publishTaskAgeMetrics(ctx, []string{"date-only.md"}, taskAgeSummaryNow(), vault)
		// 2026-10-29T00:00:00Z to 2026-10-31T12:00:00Z is 2 days 12 hours.
		Expect(taskAgeSummaryOldest(vault)).To(Equal(216000.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucket1d7d)).To(Equal(1.0))
		Expect(taskAgeSummaryBucket(vault, metrics.TaskAgeBucketUndated)).To(Equal(0.0))
	})
})
