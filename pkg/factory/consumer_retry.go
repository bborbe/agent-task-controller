// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"time"

	"github.com/bborbe/run"
	"github.com/golang/glog"
)

// retryConsumerDelay is the pause before re-entering the command consumer after a
// transient failure.
const retryConsumerDelay = 5 * time.Second

// RetryConsumer wraps a command consumer so a transient Kafka failure reconnects
// instead of terminating the process.
//
// The consumer is expected to run for the life of the pod, so any return is a
// transport-level interruption — `kafka: error while consuming <topic>/<partition>:
// EOF`, `broker not connected` — and not a reason to exit. Propagating it reaches
// service.Run, which treats the error as fatal: the controller stops scanning and
// publishing until Kubernetes restarts it, every in-flight command waits out the
// restart, and the request topic accumulates lag that is then drained one message
// at a time. Measured 2026-10-22: six restarts in 5h31m, each preceded by a consume
// EOF at a lag of 257–265.
//
// The retry stops only when ctx is done, so a shutdown is a clean nil rather than
// an error the caller would have to special-case.
func RetryConsumer(consumer run.Func, delay time.Duration) run.Func {
	return func(ctx context.Context) error {
		for {
			err := consumer(ctx)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			glog.Warningf("command consumer returned error, reconnecting in %s: %v", delay, err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}
}
