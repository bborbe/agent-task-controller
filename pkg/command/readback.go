// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// DefaultReadbackDeadline is how long a write is given to become visible before
// the readback calls it a mismatch.
//
// It is deliberately larger than git-rest's commit window: git-rest buffers
// writes and commits them on a ~30 s cadence (measured 2026-11-16 on nuke dev —
// commits at 00:40:05, 00:40:35, 00:41:05, 00:41:35, 00:42:05), so the record
// does not reach the store until the next flush. A deadline shorter than that
// window would report a mismatch on every ordinary write, which is worse than no
// detector: a signal that always fires is not a signal. 90 s is three commit
// windows — enough for a slow flush, far below the seven-hour gap this detector
// exists to catch.
const DefaultReadbackDeadline = 90 * time.Second

// DefaultReadbackInterval is how often pending expectations are checked. It is
// smaller than the deadline so an expectation is never evaluated late enough to
// hide the actual landing time.
const DefaultReadbackInterval = 5 * time.Second

// ReadbackExpectation is one write awaiting verification: the record it
// addressed, the operation that reported success, and the content as it stood
// BEFORE the write.
//
// BeforeHash is the point of the type. The question the readback asks is not
// "does the record hold what I wrote" — a later write may legitimately have
// replaced it — but "did the record MOVE at all". A fingerprint of the pre-write
// record answers that question, and it is what makes the check robust to
// supersede: the healthcheck probe's whole lifecycle (reopen → in_progress →
// done) can complete inside one commit window, so the intermediate state this
// command wrote is often never committed, while the record plainly moved.
type ReadbackExpectation struct {
	// TaskIdentifier is the record the command addressed.
	TaskIdentifier string
	// Operation is the command operation, used as the metric label.
	Operation string
	// BeforeHash is the recordHash fingerprint of the WHOLE record (frontmatter
	// and body) as it stood before the write. Empty means the record did not
	// exist (or was terminal) before the write, so ANY readable record counts as
	// landed.
	BeforeHash string
	// Deadline is when the expectation is evaluated. Nothing is dropped earlier:
	// a record that lands before the deadline simply passes when it is read.
	Deadline time.Time
}

// WriteVerifier re-reads a record after a command has reported success and
// reports a mismatch when the record did not move.
//
// It exists because a command's success result is emitted when the write is
// ACCEPTED, not when it lands. git-rest buffers the write and commits on a
// cadence, so a consumer that trusts the result is, for up to one commit window,
// reading a write that is not yet in the store. The verification cannot run
// inline: the commit trails the acknowledgement by up to 30 s, so a synchronous
// readback would either fail every ordinary write or block the command consumer
// for a commit window. It is therefore deferred, and the counter is what makes
// the gap observable.
//
//counterfeiter:generate -o ../../mocks/write_verifier.go --fake-name WriteVerifier . WriteVerifier
type WriteVerifier interface {
	// Expect records that a write reported success and must be verified.
	Expect(ctx context.Context, expectation ReadbackExpectation)
	// Run verifies due expectations until ctx is cancelled.
	Run(ctx context.Context) error
}

// NewWriteVerifier returns a WriteVerifier backed by the typed vault API.
//
// A nil clock uses the wall clock; a zero interval or deadline takes the
// package default. Both are injectable so a test can drive the loop without
// sleeping.
func NewWriteVerifier(
	client vaultapi.Client,
	m metrics.Metrics,
	clock libtime.CurrentDateTimeGetter,
	interval time.Duration,
	deadline time.Duration,
) WriteVerifier {
	if interval <= 0 {
		interval = DefaultReadbackInterval
	}
	if deadline <= 0 {
		deadline = DefaultReadbackDeadline
	}
	return &writeVerifier{
		client:   client,
		metrics:  m,
		clock:    clock,
		interval: interval,
		deadline: deadline,
	}
}

type writeVerifier struct {
	client   vaultapi.Client
	metrics  metrics.Metrics
	clock    libtime.CurrentDateTimeGetter
	interval time.Duration
	deadline time.Duration

	mu      sync.Mutex
	pending []ReadbackExpectation
}

// Expect records a write for verification at now+deadline.
func (v *writeVerifier) Expect(ctx context.Context, expectation ReadbackExpectation) {
	if expectation.TaskIdentifier == "" || expectation.Operation == "" {
		// A caller that cannot name the record cannot verify it; dropping the
		// expectation is honest, and a panic on the command path would be worse.
		glog.Warningf(
			"readback: dropped expectation with empty identifier or operation (operation=%q)",
			expectation.Operation,
		)
		return
	}
	if expectation.Deadline.IsZero() {
		expectation.Deadline = v.now().Add(v.deadline)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = append(v.pending, expectation)
}

// Run verifies due expectations on a fixed tick until ctx is cancelled.
//
// It returns nil on shutdown, not ctx.Err(): this repo runs every component
// through service.Run, which treats a component error as fatal — a verifier
// reporting context.Canceled would make an ordinary shutdown look like a crash.
func (v *writeVerifier) Run(ctx context.Context) error {
	ticker := time.NewTicker(v.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			v.verifyDue(ctx)
		}
	}
}

// verifyDue evaluates every expectation whose deadline has passed. It never
// blocks the caller: a read error other than not-found is logged and the
// expectation is dropped rather than retried, because a verifier that retries
// forever on a broken dependency hides the very gap it measures.
func (v *writeVerifier) verifyDue(ctx context.Context) {
	now := v.now()
	v.mu.Lock()
	due := make([]ReadbackExpectation, 0, len(v.pending))
	keep := v.pending[:0]
	for _, expectation := range v.pending {
		if now.Before(expectation.Deadline) {
			keep = append(keep, expectation)
			continue
		}
		due = append(due, expectation)
	}
	v.pending = keep
	v.mu.Unlock()

	for _, expectation := range due {
		if ctx.Err() != nil {
			return
		}
		v.verify(ctx, expectation)
	}
}

// verify reads the record once and decides whether it moved.
func (v *writeVerifier) verify(ctx context.Context, expectation ReadbackExpectation) {
	task, err := v.client.ReadTask(ctx, expectation.TaskIdentifier)
	if err != nil {
		if errors.Is(err, vaultapi.ErrNotFound) {
			// A record that is still absent after the deadline never landed.
			v.mismatch(expectation, "record is not readable")
			return
		}
		glog.Warningf(
			"readback: cannot verify %s for task %s: %v",
			expectation.Operation,
			expectation.TaskIdentifier,
			err,
		)
		return
	}
	if task == nil {
		v.mismatch(expectation, "record read returned no task")
		return
	}
	if expectation.BeforeHash == "" {
		// The write created the record; any readable record is the landing.
		return
	}
	afterHash, hashErr := recordHash(task.Frontmatter, task.Body)
	if hashErr != nil {
		glog.Warningf(
			"readback: cannot hash frontmatter for task %s: %v",
			expectation.TaskIdentifier,
			hashErr,
		)
		return
	}
	if afterHash == expectation.BeforeHash {
		v.mismatch(expectation, "frontmatter is unchanged since the write")
	}
}

// mismatch counts and reports one command that reported success without landing.
func (v *writeVerifier) mismatch(expectation ReadbackExpectation, reason string) {
	v.metrics.ReadbackMismatchTotal(expectation.Operation).Inc()
	glog.Errorf(
		"readback: %s reported success for task %s but the record did not move by the deadline (%s)",
		expectation.Operation,
		expectation.TaskIdentifier,
		reason,
	)
}

func (v *writeVerifier) now() time.Time {
	if v.clock == nil {
		return time.Now()
	}
	// libtime.DateTime is a named time.Time, so the conversion is exact.
	return time.Time(v.clock.Now())
}

// recordReadbackExpectation records the post-write verification for a command
// that actually wrote.
//
// It exists so a write executor does not have to carry the hashing, the nil
// verifier check and the error branch inline — the shape is identical in every
// executor, and repeating it pushed the update-frontmatter constructor over the
// function-length limit. Callers must only invoke it when a write was ISSUED:
// the expectation means "this command's effect must become visible", and a
// no-op command's effect is that nothing changes.
//
// The hash covers the whole record (frontmatter AND body), so a body-only write
// moves the fingerprint and is not mistaken for a dropped write.
func recordReadbackExpectation(
	ctx context.Context,
	verifier WriteVerifier,
	operation string,
	taskIdentifier string,
	task *vaultapi.Task,
) {
	if verifier == nil || task == nil {
		return
	}
	beforeHash, err := recordHash(task.Frontmatter, task.Body)
	if err != nil {
		glog.Warningf(
			"%s: cannot hash record for task %s: %v",
			operation,
			taskIdentifier,
			err,
		)
		return
	}
	verifier.Expect(ctx, ReadbackExpectation{
		TaskIdentifier: taskIdentifier,
		Operation:      operation,
		BeforeHash:     beforeHash,
	})
}

// recordHash returns a stable fingerprint of a whole record — frontmatter AND
// body.
//
// The body is included because `update-frontmatter` can write the body alone:
// `PutBody` changes no frontmatter key, so a frontmatter-only comparison would
// read a successful body write as "the record did not move" and count it. The
// question is whether the RECORD moved, and a record is both halves.
//
// encoding/json sorts map keys, so the encoding is canonical for a given set of
// fields and values — two reads of unchanged content fingerprint identically,
// which is what makes "did the record move" answerable without knowing which
// fields a given command wrote. The digest is stored rather than the encoded
// content: the verifier holds one entry per in-flight write, and retaining whole
// records for that is pointless when only equality is ever asked.
func recordHash(frontmatter map[string]any, body string) (string, error) {
	encoded, err := json.Marshal(struct {
		Frontmatter map[string]any `json:"frontmatter"`
		Body        string         `json:"body"`
	}{Frontmatter: frontmatter, Body: body})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
