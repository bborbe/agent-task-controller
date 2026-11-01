// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package scanner's census is unexported, so these specs live in the package
// itself rather than in scanner_test. They read the published gauges through
// prometheus.DefaultGatherer because the census writes into the process-wide
// registry that metrics.New() returns.
package scanner

import (
	"context"
	"os"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// openTaskCensusScanner builds a scanner over an in-memory file set. listFiles
// iterates the maps live, so a spec can delete a key between passes. A path in
// unreadable is listed but fails on read, exactly like the sibling
// taskAgeSummaryScanner; pass nil when every listed file is readable.
func openTaskCensusScanner(
	vaultName string,
	files map[string]string,
	unreadable map[string]struct{},
) *vaultScanner {
	return &vaultScanner{
		metrics:   metrics.New(),
		vaultName: vaultName,
		ops: fileOps{
			listFiles: func(_ context.Context, _ string) ([]string, error) {
				paths := make([]string, 0, len(files)+len(unreadable))
				for relPath := range files {
					paths = append(paths, relPath)
				}
				for relPath := range unreadable {
					paths = append(paths, relPath)
				}
				sort.Strings(paths)
				return paths, nil
			},
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

// openTaskCensusRecord builds a task file from raw frontmatter lines, so a spec
// can omit a key or repeat one.
func openTaskCensusRecord(lines ...string) string {
	content := "---\n"
	for _, line := range lines {
		content += line + "\n"
	}
	return content + "---\n# Task\n"
}

// openTaskCensusGathered returns every gathered census sample keyed by
// vault\x00phase\x00task_type. It gathers rather than reading the accessor,
// because WithLabelValues on a GaugeVec CREATES the series and would make every
// absence assertion vacuous.
func openTaskCensusGathered() map[string]float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	result := make(map[string]float64)
	for _, mf := range mfs {
		if mf.GetName() != "agent_controller_open_tasks" {
			continue
		}
		for _, sample := range mf.GetMetric() {
			labels := make(map[string]string, len(sample.GetLabel()))
			for _, lp := range sample.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			key := labels["vault"] + "\x00" + labels["phase"] + "\x00" + labels["task_type"]
			result[key] = sample.GetGauge().GetValue()
		}
	}
	return result
}

func openTaskCensusValue(vault, phase, taskType string) (float64, bool) {
	value, ok := openTaskCensusGathered()[vault+"\x00"+phase+"\x00"+taskType]
	return value, ok
}

// Every spec below uses its own vault name. The gauge is package-level and every
// spec reads the same registry, so a unique vault value keeps an absence
// assertion from depending on spec order.
var _ = Describe("open task census", func() {
	ctx := context.Background()

	It("publishes a level, not an accumulating count (AC 4)", func() {
		const vault = "census-idempotent"
		files := map[string]string{
			"a.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"b.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)
		first, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(first).To(Equal(2.0))

		v.recomputeOpenTaskCensus(ctx)
		second, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(second).To(Equal(first))
		Expect(second).To(Equal(2.0))
	})

	It("falls by exactly one when a record closes (AC 5)", func() {
		const vault = "census-fall-by-one"
		files := map[string]string{
			"p1.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"p2.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"q1.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: execution",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)
		value, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(2.0))

		delete(files, "p1.md")
		v.recomputeOpenTaskCensus(ctx)
		value, ok = openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(1.0))
		other, ok := openTaskCensusValue(vault, "execution", "pr-review")
		Expect(ok).To(BeTrue())
		Expect(other).To(Equal(1.0))
	})

	It("removes a pair when its last record closes (AC 6)", func() {
		const vault = "census-absent-on-last"
		files := map[string]string{
			"p.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"q.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: execution",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)
		p, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(p).To(Equal(1.0))

		delete(files, "p.md")
		v.recomputeOpenTaskCensus(ctx)
		_, ok = openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeFalse())
		q, ok := openTaskCensusValue(vault, "execution", "pr-review")
		Expect(ok).To(BeTrue())
		Expect(q).To(Equal(1.0))
	})

	It("clears every pair when the board empties (AC 7)", func() {
		const vault = "census-empty-board"
		files := map[string]string{
			"a.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"b.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: execution",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		clear(files)
		v.recomputeOpenTaskCensus(ctx)

		prefix := vault + "\x00"
		for key := range openTaskCensusGathered() {
			Expect(key).NotTo(HavePrefix(prefix))
		}
	})

	It("never touches another vault's series (AC 8)", func() {
		metrics.New().OpenTasks().
			WithLabelValues("census-other-vault", "planning", "x").
			Set(7)

		const vault = "census-isolated"
		v := openTaskCensusScanner(vault, map[string]string{}, nil)
		v.recomputeOpenTaskCensus(ctx)

		value, ok := openTaskCensusValue("census-other-vault", "planning", "x")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(7.0))
	})

	It("counts human_review, the resting phase is the alert's concern (AC 9)", func() {
		const vault = "census-human-review"
		files := map[string]string{
			"a.md": openTaskCensusRecord(
				"status: human_review",
				"phase: human_review",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		value, ok := openTaskCensusValue(vault, "human_review", "pr-review")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(1.0))
	})

	It("excludes only completed and aborted (AC 10)", func() {
		const vault = "census-terminal"
		files := map[string]string{
			"completed.md": openTaskCensusRecord(
				"status: completed",
				"phase: done-phase",
				"task_type: done-type",
			),
			"aborted.md": openTaskCensusRecord(
				"status: aborted",
				"phase: aborted-phase",
				"task_type: aborted-type",
			),
			"open.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: execution",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		value, ok := openTaskCensusValue(vault, "execution", "pr-review")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(1.0))
		_, ok = openTaskCensusValue(vault, "done-phase", "done-type")
		Expect(ok).To(BeFalse())
		_, ok = openTaskCensusValue(vault, "aborted-phase", "aborted-type")
		Expect(ok).To(BeFalse())
	})

	It("counts a missing and an unknown status as open (AC 11)", func() {
		const vault = "census-unknown-status"
		files := map[string]string{
			"missing.md": openTaskCensusRecord(
				"phase: planning",
				"task_type: github-update-go",
			),
			"bogus.md": openTaskCensusRecord(
				"status: bogus",
				"phase: execution",
				"task_type: pr-review",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		missing, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(missing).To(Equal(1.0))
		bogus, ok := openTaskCensusValue(vault, "execution", "pr-review")
		Expect(ok).To(BeTrue())
		Expect(bogus).To(Equal(1.0))
	})

	It("publishes empty label values instead of dropping the record (AC 12)", func() {
		const vault = "census-empty-labels"
		files := map[string]string{
			"no-phase.md": openTaskCensusRecord(
				"status: in_progress",
				"task_type: github-update-go",
			),
			"no-type.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		noPhase, ok := openTaskCensusValue(vault, "", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(noPhase).To(Equal(1.0))
		noType, ok := openTaskCensusValue(vault, "planning", "")
		Expect(ok).To(BeTrue())
		Expect(noType).To(Equal(1.0))
	})

	It("deduplicates repeated keys, keeping the last value (AC 13)", func() {
		const vault = "census-duplicate-keys"
		files := map[string]string{
			"dup.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"phase: execution",
				"task_type: github-update-go",
			),
		}
		v := openTaskCensusScanner(vault, files, nil)
		v.recomputeOpenTaskCensus(ctx)

		execution, ok := openTaskCensusValue(vault, "execution", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(execution).To(Equal(1.0))
		_, ok = openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeFalse())

		count := 0
		for key := range openTaskCensusGathered() {
			if len(key) >= len(vault) && key[:len(vault)] == vault {
				count++
			}
		}
		Expect(count).To(Equal(1))
	})

	It("labels each series with its constructor's vault name verbatim (AC 14)", func() {
		files := map[string]string{
			"a.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
		}
		openclaw := openTaskCensusScanner("census-vault-openclaw", files, nil)
		openclaw.recomputeOpenTaskCensus(ctx)
		personal := openTaskCensusScanner("census-vault-personal", files, nil)
		personal.recomputeOpenTaskCensus(ctx)

		openclawValue, ok := openTaskCensusValue(
			"census-vault-openclaw", "planning", "github-update-go",
		)
		Expect(ok).To(BeTrue())
		Expect(openclawValue).To(Equal(1.0))
		personalValue, ok := openTaskCensusValue(
			"census-vault-personal", "planning", "github-update-go",
		)
		Expect(ok).To(BeTrue())
		Expect(personalValue).To(Equal(1.0))

		// Both scanners publish into one registry, so "the other value is absent"
		// cannot hold; the invariant is one series per name and no third name.
		count := 0
		for key := range openTaskCensusGathered() {
			if len(key) >= len("census-vault-") && key[:len("census-vault-")] == "census-vault-" {
				count++
			}
		}
		Expect(count).To(Equal(2))
	})

	It("skips a broken file without blanking the instrument", func() {
		const vault = "census-broken-files"
		files := map[string]string{
			"good.md": openTaskCensusRecord(
				"status: in_progress",
				"phase: planning",
				"task_type: github-update-go",
			),
			"no-frontmatter.md": "# no frontmatter\n",
			"scalar.md":         "---\n- a\n- b\n---\n# Task\n",
		}
		unreadable := map[string]struct{}{"missing.md": {}}
		v := openTaskCensusScanner(vault, files, unreadable)
		v.recomputeOpenTaskCensus(ctx)

		value, ok := openTaskCensusValue(vault, "planning", "github-update-go")
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(1.0))

		count := 0
		for key := range openTaskCensusGathered() {
			if len(key) >= len(vault) && key[:len(vault)] == vault {
				count++
			}
		}
		Expect(count).To(Equal(1))
	})
})
