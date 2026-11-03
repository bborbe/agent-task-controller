// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// parseFrontmatterMap extracts, deduplicates and unmarshals a task file's
// frontmatter into a generic map. Both instruments read their facts through
// here, so a single-pass recompute parses each file exactly once.
//
// It returns false on the three skip conditions both instruments previously
// applied when each parsed independently: no frontmatter, frontmatter that will
// not deduplicate, and frontmatter that will not unmarshal.
func parseFrontmatterMap(ctx context.Context, content []byte) (map[string]interface{}, bool) {
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		return nil, false
	}
	deduped, _, err := DeduplicateFrontmatter(ctx, fmYAML)
	if err != nil {
		return nil, false
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(deduped), &fmMap); err != nil {
		return nil, false
	}
	return fmMap, true
}

// recomputeVaultMetrics publishes the task-age series and the open-task census
// from ONE listing and ONE read per task file.
//
// The two instruments previously ran as independent passes, each listing the task
// directory and fetching every file over the vault API. On the openclaw vault's
// ~22,000 task files that cost ~730 GET /api/v1/files/{path} per second, twice the
// rate this pass needs. Splitting parse from classification (taskAgeFromMap,
// openTaskPairFromMap) keeps the published values identical — only the number of
// reads changes.
//
// A failed listing writes nothing at all: the previous pass's values stay in
// place, exactly as each instrument behaved on its own. A file that cannot be read
// or parsed is skipped by both instruments, matching their per-file behaviour.
func (v *vaultScanner) recomputeVaultMetrics(ctx context.Context) {
	now := time.Now()
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("metrics recompute: list %s failed: %v", glob, err)
		return
	}

	ageCounts := make(map[metrics.TaskAgeBucket]int, len(metrics.AvailableTaskAgeBuckets))
	oldest := time.Duration(0)
	censusCounts := make(map[openTaskPair]int, len(v.openTaskPairs))

	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return
		default:
		}
		content, readErr := v.ops.readFile(ctx, relPath)
		if readErr != nil {
			continue
		}
		fmMap, ok := parseFrontmatterMap(ctx, content)
		if !ok {
			continue
		}

		switch age, state := taskAgeFromMap(fmMap, now); state {
		case taskAgeTerminal, taskAgeSkipped:
		case taskAgeUndated:
			ageCounts[metrics.TaskAgeBucketUndated]++
		case taskAgeDated:
			ageCounts[taskAgeBucketOf(age)]++
			if age > oldest {
				oldest = age
			}
		}

		if pair, open := openTaskPairFromMap(fmMap); open {
			censusCounts[pair]++
		}
	}

	v.publishTaskAgeCounts(ageCounts, oldest, v.vaultName)
	v.publishCensusCounts(censusCounts)
}
