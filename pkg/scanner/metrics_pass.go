// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	lib "github.com/bborbe/agent"
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
//
// It returns the paths whose frontmatter is capped, still assigned and therefore a
// candidate for the capped-idle sweep, the number of files it listed, and whether the
// pass completed — false on a failed listing or a cancelled context, where the caller
// must not treat an empty candidate set as "nothing to escalate".
// It does not escalate them itself: this function stays read-only, and the caller
// (sweepCappedIdle) performs the writes. Collecting the candidates here rather than
// in a pass of its own is what makes the sweep cost nothing extra — this pass
// already lists the directory and reads and parses every file, so a second listing
// and a second read of every file would buy nothing and cost a full extra pass over
// the vault.
func (v *vaultScanner) recomputeVaultMetrics(
	ctx context.Context,
) (candidates []string, fileCount int, ok bool) {
	now := time.Now()
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("metrics recompute: list %s failed: %v", glob, err)
		return nil, 0, false
	}

	ageCounts := make(map[metrics.TaskAgeBucket]int, len(metrics.AvailableTaskAgeBuckets))
	oldest := time.Duration(0)
	censusCounts := make(map[openTaskPair]int, len(v.openTaskPairs))
	var cappedAssigned []string

	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, 0, false
		default:
		}
		content, readErr := v.ops.readFile(ctx, relPath)
		if readErr != nil {
			continue
		}
		fmMap, parsed := parseFrontmatterMap(ctx, content)
		if !parsed {
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

		// The candidate list the periodic sweep escalates. The clause is the cheap
		// pre-filter only: escalateCappedContent re-checks every clause against the
		// on-disk file at write time, so a stale entry here is a no-op.
		if fm := lib.TaskFrontmatter(fmMap); isCappedIdle(fm) && fm.Assignee() != "" {
			cappedAssigned = append(cappedAssigned, relPath)
		}
	}

	v.publishTaskAgeCounts(ageCounts, oldest, v.vaultName)
	v.publishCensusCounts(censusCounts)
	return cappedAssigned, len(paths), true
}
