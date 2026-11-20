// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
)

// fileFacts is one task file's classification, split into the parts that are
// stable for as long as the file is unchanged and the anchors the time-dependent
// parts are recomputed from.
//
// The split is the whole point of the cache. Every instrument the metrics pass
// publishes is one of two kinds:
//
//   - Time-independent — the open-task census pair, the parked-probe gauge and the
//     capped-idle candidate test read only frontmatter values. Their answer cannot
//     change without the file changing, so it is computed once and reused.
//   - Time-dependent — task age and the stuck-planning gauge compare an anchor
//     against `now`. Caching their *answer* would freeze a record's age at the
//     value it had when last read, so only the anchor and the stable preconditions
//     are cached and the comparison is redone on every pass.
//
// No file body is retained: the only body fact any instrument needs is whether a
// `## Failure` section is present, which is a bool here.
type fileFacts struct {
	// updatedAt is the listing stamp this entry was built from. It is the
	// invalidation key: a pass reuses the entry only when the fresh stamp is
	// non-nil and equal to this one. A nil on either side means the file is read
	// again — an unknown stamp can never prove a file unchanged.
	updatedAt *time.Time

	// skipped marks a file that could not be read, carried no frontmatter, or
	// carried frontmatter that would not parse. It contributes to no instrument,
	// exactly as the per-file skip did before the cache existed.
	skipped bool

	// ageTerminal is a completed or aborted record: counted in no age bucket.
	ageTerminal bool
	// ageAnchor is the first parseable timestamp in the age precedence order, and
	// ageHasAnchor reports whether one was found. Neither present nor terminal
	// means the undated bucket.
	ageAnchor    time.Time
	ageHasAnchor bool

	// censusPair and censusOpen are the open-task census contribution.
	censusPair openTaskPair
	censusOpen bool

	// parked is the parked-probe gauge contribution.
	parked bool

	// capped is the capped-idle candidate test. It is deliberately time-independent
	// (see isCappedIdle): a record becomes a candidate through a frontmatter change,
	// never through elapsed time, so an unchanged file cannot become one.
	capped bool

	// stuckPre holds every stuck-planning clause except the age comparison: the
	// task type, phase, status and `## Failure` body section. stuckStartedAt is the
	// parsed job start. The gauge fires only when stuckPre holds AND the start is
	// older than stuckPlanningMaxAge, which is recomputed against `now` each pass.
	stuckPre       bool
	stuckHasStart  bool
	stuckStartedAt time.Time
}

// factsOf classifies one parsed task file. Pure — no I/O, and no clock read — so a
// test can build the expected facts from a frontmatter map and content directly,
// and so the time-dependent instruments stay a caller-side comparison rather than
// a value frozen at classification time.
func factsOf(fmMap map[string]interface{}, content []byte, updatedAt *time.Time) fileFacts {
	facts := fileFacts{updatedAt: updatedAt}

	switch anchor, state := taskAgeAnchorOf(fmMap); state {
	case taskAgeTerminal:
		facts.ageTerminal = true
	case taskAgeDated:
		facts.ageAnchor = anchor
		facts.ageHasAnchor = true
	case taskAgeUndated, taskAgeSkipped:
	}

	if pair, open := openTaskPairFromMap(fmMap); open {
		facts.censusPair = pair
		facts.censusOpen = true
	}

	facts.parked = isParkedProbe(fmMap)
	facts.capped = isCappedIdle(lib.TaskFrontmatter(fmMap))

	if stringField(fmMap, "task_type") == stuckPlanningTaskType &&
		stringField(fmMap, "phase") == "planning" &&
		stringField(fmMap, "status") == "in_progress" {
		if startedAt, err := time.Parse(
			time.RFC3339, stringField(fmMap, "job_started_at"),
		); err == nil {
			facts.stuckHasStart = true
			facts.stuckStartedAt = startedAt
		}
		facts.stuckPre = strings.Contains(extractBody(content), "\n## Failure")
	}

	return facts
}

// factsFor returns relPath's facts and whether the file was read to produce them.
// It re-reads and re-classifies only when the listing stamp says the file may have
// changed since the cached entry was built; a cached entry reports read=false.
//
// The cache is filled by the caller's pass and never mutated in place: a pass
// builds a fresh map so a file deleted from the vault cannot leave an entry behind
// to keep contributing to a gauge.
func (v *vaultScanner) factsFor(
	ctx context.Context,
	relPath string,
	cached map[string]fileFacts,
) (fileFacts, bool) {
	// A seam without updatedAt — an older fake, or a caller that has no listing
	// stamp — reports nil, which the comparison below can never satisfy. The file is
	// read, exactly as it was before the cache existed.
	var stamp *time.Time
	if v.ops.updatedAt != nil {
		stamp = v.ops.updatedAt(relPath)
	}
	if previous, ok := cached[relPath]; ok && stamp != nil &&
		previous.updatedAt != nil && previous.updatedAt.Equal(*stamp) {
		return previous, false
	}

	content, err := v.ops.readFile(ctx, relPath)
	if err != nil {
		// A skip is deliberately NOT stamped. Caching it would pin the file as
		// unreadable until something happened to move its listing stamp, so a
		// transient read error or a half-written file would drop out of every later
		// candidate set — the starvation a bounded pass must not introduce. Leaving
		// the stamp nil means the next pass reads it again.
		return fileFacts{skipped: true}, true
	}
	fmMap, parsed := parseFrontmatterMap(ctx, content)
	if !parsed {
		return fileFacts{skipped: true}, true
	}
	return factsOf(fmMap, content, stamp), true
}
