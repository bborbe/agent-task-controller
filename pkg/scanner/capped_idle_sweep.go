// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	"github.com/golang/glog"
)

// sweepCappedIdle runs one periodic pass of the capped-idle sweep over a candidate
// set that this same pass rebuilds.
//
// The boot sweep in buildIndex is a one-shot. It collects its candidates into a
// local slice that dies with the function, and on a trigger-only controller
// (POLL_INTERVAL=0s, which is what prod runs) the alternative source, v.hashes, is
// never populated at all. A record that crosses its cap after boot — or a task file
// created after boot — therefore reached no sweep and waited for the next pod
// restart. Measured on prod 2026-10-28: zero escalateCappedIdle lines over two hours
// while fifteen capped records still held their assignees, so the escalation was a
// manual restart ritual.
//
// This is the recurrence. It rides the metrics recompute pass, whose ticker already
// runs once at start-up and then every metricsInterval — a package constant in
// main.go, independent of POLL_INTERVAL — so the candidate set is rebuilt from a
// fresh listing and a fresh read of every file on every pass. That pass is also why
// the sweep costs nothing extra: it already lists the task directory and reads and
// parses every file for the age and census series, so a sweep with its own listing
// would double the vault read rate for facts this pass has already paid for.
//
// The escalation itself stays in escalateCappedPaths, so the per-path clauses live
// in one place and the scan-cycle arm, the boot arm and this one cannot drift.
//
// It logs one summary line on EVERY pass, escalating or not, plus one line per
// escalated record. The unconditional summary is deliberate: a pass that logs only
// when it escalates is indistinguishable from a pass that never ran, and "the sweep
// is silent" is exactly the observation that let the boot-snapshot limitation go
// unnoticed on prod for a week. One line every five minutes is the price of being
// able to say the sweep is alive and how long it took.
func (v *vaultScanner) sweepCappedIdle(ctx context.Context) {
	start := time.Now()
	candidates, fileCount, ok := v.recomputeVaultMetrics(ctx)
	if !ok {
		// A failed listing or a cancelled context: recomputeVaultMetrics has already
		// logged the former, and the latter needs no log. An empty candidate set here
		// would read as "nothing to escalate", which is not what happened.
		return
	}
	escalated, writeError := v.escalateCappedPaths(ctx, candidates)
	glog.Infof(
		"escalateCappedIdle: sweep pass over %d file(s) in %v (%d candidate(s), %d escalated)",
		fileCount,
		time.Since(start),
		len(candidates),
		escalated,
	)
	if writeError {
		glog.Warningf("escalateCappedIdle: sweep pass finished with write errors")
	}
}
