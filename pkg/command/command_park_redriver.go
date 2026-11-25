// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// Default park re-drive timings. The delay is the pause between parking a
// command and its first re-drive attempt: an outage short enough to clear on its
// own is then absorbed by one or two attempts instead of hammering a vault-api
// that is already overloaded. The interval is the cadence of the whole sweep.
const (
	DefaultParkRedriveDelay    = 30 * time.Second
	DefaultParkRedriveInterval = 30 * time.Second
)

//counterfeiter:generate -o ../../mocks/park_redriver.go --fake-name ParkRedriver . ParkRedriver

// ParkRedriver re-executes commands parked by NewTransientAwareExecutorTx and
// removes them once they are decided.
//
// A parked command is re-driven at most every interval and only after the park
// is redriveDelay old, so a vault-api outage produces one attempt per interval
// rather than a retry storm. A command still undecided when the park is
// expireDuration old is dropped with an unconditional Error line and a
// CommandExpiredTotal increment — the same visible terminus the expiry filter
// gives an unprocessed message, so a command the controller could not execute is
// never lost silently.
type ParkRedriver interface {
	// Run blocks until ctx is cancelled, sweeping once per interval.
	Run(ctx context.Context) error
	// RedriveOnce performs a single sweep. Exposed so tests do not manage tickers.
	RedriveOnce(ctx context.Context) error
}

// NewParkRedriver builds the re-drive loop for one executor. Only operations
// whose execution is safe to repeat are parked by the executor wrapper, so this
// loop does not need to know which operation it is driving.
func NewParkRedriver(
	park CommandPark,
	executor cdb.CommandObjectExecutorTx,
	schemaID cdb.SchemaID,
	resultObjectSender cdb.ResultObjectSender,
	expireDuration time.Duration,
	redriveDelay time.Duration,
	interval time.Duration,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
) ParkRedriver {
	return &parkRedriver{
		park:               park,
		executor:           executor,
		schemaID:           schemaID,
		resultObjectSender: resultObjectSender,
		expireDuration:     expireDuration,
		redriveDelay:       redriveDelay,
		interval:           interval,
		currentDateTime:    currentDateTime,
		metrics:            m,
	}
}

type parkRedriver struct {
	park               CommandPark
	executor           cdb.CommandObjectExecutorTx
	schemaID           cdb.SchemaID
	resultObjectSender cdb.ResultObjectSender
	expireDuration     time.Duration
	redriveDelay       time.Duration
	interval           time.Duration
	currentDateTime    libtime.CurrentDateTimeGetter
	metrics            metrics.Metrics
}

func (r *parkRedriver) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	glog.V(2).Infof(
		"parked command re-drive started interval=%s delay=%s expire=%s",
		r.interval,
		r.redriveDelay,
		r.expireDuration,
	)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.RedriveOnce(ctx); err != nil {
				// A store failure must not tear down the controller: the parked
				// commands are still on disk and the next tick retries them.
				glog.Errorf("parked command re-drive tick: %v", err)
			}
		}
	}
}

// RedriveOnce performs one pass over the parked commands.
func (r *parkRedriver) RedriveOnce(ctx context.Context) error {
	parked, err := r.park.List(ctx)
	if err != nil {
		// commandPark.List already names the operation; wrapping again only
		// doubles the message.
		return err
	}
	if len(parked) == 0 {
		return nil
	}
	now := r.currentDateTime.Now().Time()
	for _, entry := range parked {
		if err := ctx.Err(); err != nil {
			return errors.Wrapf(
				ctx,
				err,
				"context done before re-driving parked command %s",
				entry.Key,
			)
		}
		if now.Sub(entry.ParkedAt) < r.redriveDelay {
			continue
		}
		// The window is measured from the command's own request time, like the
		// consumer's expiry filter, so a command cannot gain a second full window
		// by sitting in the queue before it was parked. A command with no request
		// time (only possible for a corrupt payload) falls back to its park time.
		age := now.Sub(entry.ParkedAt)
		if !entry.Command.RequestTime.IsZero() {
			age = now.Sub(entry.Command.RequestTime)
		}
		if age > r.expireDuration {
			r.drop(ctx, entry)
			continue
		}
		if err := r.redrive(ctx, entry); err != nil {
			glog.Warningf(
				"parked command %s (task=%s) re-drive failed: %v",
				entry.Key,
				entry.TaskIdentifier,
				err,
			)
		}
	}
	return nil
}

// redrive executes one parked command and settles its parked entry: success and
// a permanent failure both decide the command and publish its result, a
// transient failure leaves it parked for the next pass.
func (r *parkRedriver) redrive(ctx context.Context, entry ParkedCommand) error {
	commandObject := cdb.CommandObject{
		SchemaID: r.schemaID,
		Command:  entry.Command,
	}
	var resultErr error
	// No transaction: the executor addresses the task through vault-api, so it
	// holds no BoltDB write here on purpose. A write transaction would stay open
	// for the length of the HTTP call — up to the client's 30 s timeout — and
	// BoltDB serialises writers, so the consumer's own batch commits and park
	// writes would queue behind every re-drive attempt.
	_, _, resultErr = r.executor.HandleCommand(ctx, nil, commandObject)

	switch {
	case resultErr == nil:
		if err := r.resultObjectSender.Send(
			ctx,
			cdb.CreateResultObjectSuccess(commandObject, nil, nil),
		); err != nil {
			return errors.Wrapf(ctx, err, "send success result for parked command %s", entry.Key)
		}
		if err := r.park.Delete(ctx, entry.Key); err != nil {
			return errors.Wrapf(ctx, err, "delete parked command %s", entry.Key)
		}
		glog.V(2).Infof(
			"parked command re-driven successfully: requestID=%s operation=%s task=%s",
			entry.Command.RequestID,
			entry.Command.Operation,
			entry.TaskIdentifier,
		)
		return nil
	case errors.Is(resultErr, cdb.ErrCommandObjectSkipped):
		// A skip is a decision, not a failure: the library publishes no result for
		// it, and neither does this loop. Reachable when a parked command no longer
		// applies after a redeploy (e.g. a cross-vault create).
		if err := r.park.Delete(ctx, entry.Key); err != nil {
			return errors.Wrapf(ctx, err, "delete skipped parked command %s", entry.Key)
		}
		glog.V(2).Infof(
			"parked command re-driven and skipped: requestID=%s operation=%s task=%s",
			entry.Command.RequestID,
			entry.Command.Operation,
			entry.TaskIdentifier,
		)
		return nil
	case errors.Is(resultErr, task.ErrTaskAlreadyExists):
		// The first attempt was ambiguous and had in fact landed: the retry found
		// the file it had already created. That is a decided success, not a
		// failure to publish — the task exists, which is what the command asked for.
		if err := r.park.Delete(ctx, entry.Key); err != nil {
			return errors.Wrapf(ctx, err, "delete parked command %s", entry.Key)
		}
		glog.V(2).Infof(
			"parked command re-driven and already materialized: requestID=%s operation=%s task=%s",
			entry.Command.RequestID,
			entry.Command.Operation,
			entry.TaskIdentifier,
		)
		return nil
	case vaultapi.IsTransient(resultErr):
		glog.V(2).Infof(
			"parked command %s (task=%s) still failing transiently, keeping it parked: %v",
			entry.Key,
			entry.TaskIdentifier,
			resultErr,
		)
		return nil
	default:
		if err := r.resultObjectSender.Send(
			ctx,
			cdb.CreateResultObjectFailure(commandObject, resultErr),
		); err != nil {
			return errors.Wrapf(ctx, err, "send failure result for parked command %s", entry.Key)
		}
		if err := r.park.Delete(ctx, entry.Key); err != nil {
			return errors.Wrapf(ctx, err, "delete parked command %s", entry.Key)
		}
		glog.Warningf(
			"parked command re-driven and failed permanently: requestID=%s operation=%s task=%s error=%v",
			entry.Command.RequestID,
			entry.Command.Operation,
			entry.TaskIdentifier,
			resultErr,
		)
		return nil
	}
}

// drop discards a parked command whose re-drive window has closed. It is the
// visible terminus: the command is gone, and the log line plus the counter say
// which task it was.
func (r *parkRedriver) drop(ctx context.Context, entry ParkedCommand) {
	glog.Errorf(
		"parked command expired and dropped without ever executing: requestID=%s operation=%s task=%s parkedAt=%s",
		entry.Command.RequestID,
		entry.Command.Operation,
		entry.TaskIdentifier,
		entry.ParkedAt.Format(time.RFC3339),
	)
	r.metrics.CommandExpiredTotal(string(entry.Command.Operation)).Inc()
	if err := r.park.Delete(ctx, entry.Key); err != nil {
		glog.Warningf("delete expired parked command %s failed: %v", entry.Key, err)
	}
}
