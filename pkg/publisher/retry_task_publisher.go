// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package publisher

import (
	"context"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// retryDelays is the exponential backoff applied between publish attempts:
// 1s, 2s, 4s, 8s. A publish is attempted once immediately and then retried
// after each delay, so one call makes at most len(retryDelays)+1 (5) attempts
// and waits at most 15s before giving up.
var retryDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

// NewRetryTaskPublisher wraps inner with bounded exponential-backoff retries so
// a transient Kafka produce failure (for example Sarama's ErrNotLeader) does not
// reach the sync loop's fail-fast path and terminate the controller. On
// exhaustion it returns the last error unchanged in cause, so the caller's
// existing fatal handling still fires.
func NewRetryTaskPublisher(
	inner TaskPublisher,
	m metrics.Metrics,
	waiter run.Waiter,
) TaskPublisher {
	return &retryTaskPublisher{inner: inner, metrics: m, waiter: waiter}
}

type retryTaskPublisher struct {
	inner   TaskPublisher
	metrics metrics.Metrics
	waiter  run.Waiter
}

func (p *retryTaskPublisher) PublishChanged(ctx context.Context, task lib.Task) error {
	return p.withRetry(ctx, "changed", func(ctx context.Context) error {
		return p.inner.PublishChanged(ctx, task)
	})
}

func (p *retryTaskPublisher) PublishDeleted(ctx context.Context, id lib.TaskIdentifier) error {
	return p.withRetry(ctx, "deleted", func(ctx context.Context) error {
		return p.inner.PublishDeleted(ctx, id)
	})
}

// withRetry runs fn once, then up to len(retryDelays) more times, waiting the
// corresponding delay between attempts. A cancelled context during a wait stops
// the retries and returns the wait error, so shutdown is not delayed. Every
// completed retry increments the retry counter; exhausting the delays increments
// the failure counter and returns the last error.
func (p *retryTaskPublisher) withRetry(
	ctx context.Context,
	outcome string,
	fn func(context.Context) error,
) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt >= len(retryDelays) {
			p.metrics.KafkaPublishFailureTotal(outcome).Inc()
			return errors.Wrapf(
				ctx, err, "publish %s failed after %d attempts", outcome, attempt+1,
			)
		}
		glog.V(2).Infof(
			"publish %s attempt %d failed: %v; retrying in %v",
			outcome, attempt+1, err, retryDelays[attempt],
		)
		if waitErr := p.waiter.Wait(ctx, retryDelays[attempt]); waitErr != nil {
			return errors.Wrapf(ctx, waitErr, "publish %s retry wait interrupted", outcome)
		}
		p.metrics.KafkaPublishRetryTotal(outcome).Inc()
	}
}
