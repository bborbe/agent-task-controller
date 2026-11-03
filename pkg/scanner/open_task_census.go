// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"

	"github.com/golang/glog"
)

// openTaskPair is the label triple, minus the vault, one census sample counts.
// The field order matches the declaration order of the gauge's labels after
// "vault", which is the argument order of WithLabelValues and DeleteLabelValues.
type openTaskPair struct {
	phase    string
	taskType string
}

// recomputeOpenTaskCensus lists the task directory once and publishes the
// open-task census from it. It never pulls, scans, writes, commits or sends on
// the results channel: it is a read-only consumer of the files the scan cycle
// already reads.
//
// A failed listing writes nothing at all. The values from the previous successful
// pass stay in place and nothing is Set to 0, because 0 would claim a clean board;
// on a pod's first failed pass nothing has been published yet, so the series are
// absent.
func (v *vaultScanner) recomputeOpenTaskCensus(ctx context.Context) {
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("open task census: list %s failed: %v", glob, err)
		return
	}
	v.publishOpenTaskCensus(ctx, paths)
}

// publishOpenTaskCensus counts the open records among paths and publishes the
// census. Counts are accumulated across the whole listing and written only after
// the loop, so a pass abandoned mid-way publishes nothing rather than a partial
// board — and the previous pass's values stay untouched.
func (v *vaultScanner) publishOpenTaskCensus(ctx context.Context, paths []string) {
	counts := make(map[openTaskPair]int, len(v.openTaskPairs))
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pair, open := v.openTaskPairFromFile(ctx, relPath)
		if !open {
			continue
		}
		counts[pair]++
	}
	v.publishCensusCounts(counts)
}

// publishCensusCounts writes the accumulated census. Split out so the single-pass
// recompute (recomputeVaultMetrics) publishes exactly this board from facts it has
// already parsed, instead of reading every file a second time.
func (v *vaultScanner) publishCensusCounts(counts map[openTaskPair]int) {
	census := v.metrics.OpenTasks()
	for pair, count := range counts {
		census.WithLabelValues(v.vaultName, pair.phase, pair.taskType).Set(float64(count))
	}
	// Delete, never Reset: three controllers run from one image, so a blanket reset
	// would clear another vault's series. DeleteLabelValues is scoped to this
	// controller's own vault value, and the three values are passed in the gauge's
	// declaration order (vault, phase, task_type).
	for pair := range v.openTaskPairs {
		if _, present := counts[pair]; present {
			continue
		}
		census.DeleteLabelValues(v.vaultName, pair.phase, pair.taskType)
	}
	v.openTaskPairs = make(map[openTaskPair]struct{}, len(counts))
	for pair := range counts {
		v.openTaskPairs[pair] = struct{}{}
	}
}

// openTaskPairFromFile classifies one task file. It returns the record's
// (phase, task_type) pair and true when the record is open, and the zero pair and
// false when the record is terminal or the file must be skipped.
//
// Only "completed" and "aborted" are terminal. Every other status — including a
// missing key, an unknown value, human_review and done — is open, because the
// resting-phase exclusion belongs to the alert expression, not the instrument.
func (v *vaultScanner) openTaskPairFromFile(
	ctx context.Context,
	relPath string,
) (openTaskPair, bool) {
	content, err := v.ops.readFile(ctx, relPath)
	if err != nil {
		return openTaskPair{}, false
	}
	fmMap, ok := parseFrontmatterMap(ctx, content)
	if !ok {
		return openTaskPair{}, false
	}
	return openTaskPairFromMap(fmMap)
}

// openTaskPairFromMap classifies an already-parsed frontmatter map. Pure — no I/O
// — so the single-pass recompute can share one parse between both instruments.
func openTaskPairFromMap(fmMap map[string]interface{}) (openTaskPair, bool) {
	status := stringField(fmMap, "status")
	if status == "completed" || status == "aborted" {
		return openTaskPair{}, false
	}
	// A missing or non-string phase/task_type contributes the empty string. No
	// normalisation, no sentinel and no filtering: the record is still counted.
	return openTaskPair{
		phase:    stringField(fmMap, "phase"),
		taskType: stringField(fmMap, "task_type"),
	}, true
}
