// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	"github.com/golang/glog"

	"github.com/bborbe/agent/task/controller/pkg/metrics"
	"github.com/bborbe/agent/task/controller/pkg/publisher"
	"github.com/bborbe/agent/task/controller/pkg/scanner"
)

//counterfeiter:generate -o ../../mocks/sync_loop.go --fake-name SyncLoop . SyncLoop

// SyncLoop orchestrates scanning and publishing of task events.
type SyncLoop interface {
	Run(ctx context.Context) error
	Trigger()
}

// NewSyncLoop creates a SyncLoop that connects scanner results to publisher calls.
func NewSyncLoop(
	scanner scanner.VaultScanner,
	publisher publisher.TaskPublisher,
	trigger chan struct{},
	metrics metrics.Metrics,
) SyncLoop {
	return &syncLoop{
		scanner:   scanner,
		publisher: publisher,
		trigger:   trigger,
		metrics:   metrics,
	}
}

type syncLoop struct {
	scanner   scanner.VaultScanner
	publisher publisher.TaskPublisher
	trigger   chan struct{}
	metrics   metrics.Metrics
}

// Trigger requests an immediate scan cycle. Non-blocking: if a trigger is already pending, it is a no-op.
func (s *syncLoop) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *syncLoop) Run(ctx context.Context) error {
	results := make(chan scanner.ScanResult, 1)
	return run.CancelOnFirstErrorWait(
		ctx,
		func(ctx context.Context) error {
			return s.scanner.Run(ctx, results)
		},
		func(ctx context.Context) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case result := <-results:
					if err := s.processResult(ctx, result); err != nil {
						return errors.Wrapf(ctx, err, "process scan result")
					}
				}
			}
		},
	)
}

func (s *syncLoop) processResult(ctx context.Context, result scanner.ScanResult) error {
	if len(result.Changed) > 0 || len(result.Deleted) > 0 {
		glog.V(2).Infof(
			"scan cycle: %d changed, %d deleted",
			len(result.Changed), len(result.Deleted),
		)
		s.metrics.ScanCyclesTotal("changes").Inc()
	} else {
		glog.V(3).Infof("scan cycle: no changes")
		s.metrics.ScanCyclesTotal("no_changes").Inc()
	}
	for _, task := range result.Changed {
		glog.V(3).Infof("publishing changed task %s", task.TaskIdentifier)
		if err := s.publisher.PublishChanged(ctx, task); err != nil {
			return errors.Wrapf(ctx, err, "publish changed task %s", task.TaskIdentifier)
		}
		s.metrics.TasksPublishedTotal("changed").Inc()
	}
	for _, id := range result.Deleted {
		glog.V(3).Infof("publishing deleted task %s", id)
		if err := s.publisher.PublishDeleted(ctx, id); err != nil {
			return errors.Wrapf(ctx, err, "publish deleted task %s", id)
		}
		s.metrics.TasksPublishedTotal("deleted").Inc()
	}
	return nil
}
