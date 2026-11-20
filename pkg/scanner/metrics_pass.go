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

// recomputeVaultMetrics publishes the task-age series, the open-task census, the
// parked-probe gauge and the update-go stuck-planning gauge from ONE listing and
// ONE read per task file — and, from the second pass on, one read per file whose
// listing stamp actually moved.
//
// The two instruments previously ran as independent passes, each listing the task
// directory and fetching every file over the vault API. On the openclaw vault's
// ~22,000 task files that cost ~730 typed reads per second, twice the
// rate this pass needs. Splitting parse from classification (taskAgeFromMap,
// openTaskPairFromMap) keeps the published values identical — only the number of
// reads changes.
//
// That single pass still read every file on every tick: 12.8 k typed reads every
// five minutes on the personal vault, 26–47 % of the interval, against the same
// client the command consumer writes through. The listing is one cheap call and
// already carries each page's `updated_at`, so a file whose stamp has not moved
// since the previous pass cannot have changed and is not read again — its facts are
// reused from metricsFacts. What is cached is the time-independent classification
// plus the anchors the time-dependent instruments measure against, never a rendered
// age: a cached record's age must keep growing.
//
// A failed listing writes nothing at all: the previous pass's values stay in
// place, exactly as each instrument behaved on its own. A file that cannot be read
// or parsed is skipped by both instruments, matching their per-file behaviour.
//
// It returns the paths whose frontmatter is capped and therefore a candidate for the
// capped-idle sweep — assigned or not, because an already-empty assignee is the stuck
// shape the sweep exists for — the number of files it listed, the number of files it
// actually read, and whether the pass completed — false on a failed listing or a
// cancelled context, where the caller must not treat an empty candidate set as
// "nothing to escalate".
// It does not escalate them itself: this function stays read-only, and the caller
// (sweepCappedIdle) performs the writes. Collecting the candidates here rather than
// in a pass of its own is what makes the sweep cost nothing extra — this pass
// already lists the directory, so a second listing would buy nothing and cost a
// full extra call over the vault.
func (v *vaultScanner) recomputeVaultMetrics(
	ctx context.Context,
) (candidates []string, fileCount int, readCount int, ok bool) {
	now := time.Now()
	glob := v.taskDir + "/*.md"
	paths, err := v.ops.listFiles(ctx, glob)
	if err != nil {
		glog.Warningf("metrics recompute: list %s failed: %v", glob, err)
		return nil, 0, 0, false
	}

	// The previous pass's facts, and the map this pass fills. A fresh map every pass
	// is deliberate: a file deleted from the vault must not leave an entry behind
	// that keeps contributing to a gauge.
	previous := v.metricsFacts
	next := make(map[string]fileFacts, len(paths))
	tally := newMetricsTally(len(v.openTaskPairs))

	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, 0, 0, false
		default:
		}
		// factsFor re-reads only a file whose listing stamp moved. Every instrument
		// is then recomputed from the facts, including the two that compare an anchor
		// against `now` — which is why the cache stores anchors and not ages.
		facts, read := v.factsFor(ctx, relPath, previous)
		next[relPath] = facts
		if read {
			readCount++
		}
		tally.add(relPath, facts, now)
	}
	v.metricsFacts = next

	v.publishTaskAgeCounts(tally.ageCounts, tally.oldest, v.vaultName)
	v.publishCensusCounts(tally.censusCounts)
	// Set last, and only on a completed pass: the early returns above must leave the
	// previous value in place rather than publish 0 for a listing that never ran.
	v.metrics.HealthcheckParkedProbes().Set(float64(tally.parkedProbes))
	v.metrics.UpdateGoStuckPlanningTasks().Set(float64(tally.stuckPlanning))
	return tally.candidates, len(paths), readCount, true
}

// metricsTally accumulates one pass's instrument contributions. It is split out of
// the pass loop so that loop stays a read-and-delegate: the classification
// decisions — including the two time-dependent comparisons against `now` — live in
// one place, beside the reason they are not cached.
type metricsTally struct {
	ageCounts     map[metrics.TaskAgeBucket]int
	oldest        time.Duration
	censusCounts  map[openTaskPair]int
	parkedProbes  int
	stuckPlanning int
	candidates    []string
}

func newMetricsTally(censusCapacity int) *metricsTally {
	return &metricsTally{
		ageCounts:    make(map[metrics.TaskAgeBucket]int, len(metrics.AvailableTaskAgeBuckets)),
		censusCounts: make(map[openTaskPair]int, censusCapacity),
	}
}

// add folds one file's facts into the pass. A skipped file contributes nothing,
// matching the per-file skip both instruments applied before the cache existed.
func (t *metricsTally) add(relPath string, facts fileFacts, now time.Time) {
	if facts.skipped {
		return
	}

	// The update-go stuck gauge used to be Set from scanFiles, which never runs on a
	// trigger-only controller — every dev and prod controller runs POLL_INTERVAL=0s —
	// so the series sat at its init value and the stuck-record signal was dead
	// everywhere. The age comparison is made here, against this pass's clock, rather
	// than cached: the same file crosses the threshold with no write to it.
	if facts.stuckPre && facts.stuckHasStart &&
		now.Sub(facts.stuckStartedAt) > stuckPlanningMaxAge {
		t.stuckPlanning++
	}
	if facts.parked {
		t.parkedProbes++
	}

	switch {
	case facts.ageTerminal:
	case facts.ageHasAnchor:
		age := now.Sub(facts.ageAnchor).Truncate(time.Second)
		if age < 0 {
			age = 0
		}
		t.ageCounts[taskAgeBucketOf(age)]++
		if age > t.oldest {
			t.oldest = age
		}
	default:
		t.ageCounts[metrics.TaskAgeBucketUndated]++
	}

	if facts.censusOpen {
		t.censusCounts[facts.censusPair]++
	}

	// The candidate list the periodic sweep escalates. The clause is the cheap
	// pre-filter only: escalateCappedContent re-checks every clause against the
	// on-disk file at write time, so a stale entry here is a no-op. Assignee is
	// deliberately not part of it: an empty assignee is the stuck shape, not a
	// reason to skip, so filtering on it here would drop exactly the rows the sweep
	// exists to park.
	if facts.capped {
		t.candidates = append(t.candidates, relPath)
	}
}
