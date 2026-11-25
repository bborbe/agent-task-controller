// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"fmt"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// NewTransientAwareExecutorTx wraps one command executor so a command the vault
// could not decide is PARKED and re-driven instead of being committed past and
// lost.
//
// The wrapper delegates everything except one case. A command that failed
// transiently is written to the park inside the handler's own transaction and is
// then reported as SKIPPED: cdb's own result-sender wrapper turns
// ErrCommandObjectSkipped into "no result, no error", the batch transaction
// commits, and ParkRedriver re-executes the command later. Every other outcome
// reaches the library's wrapper unchanged, so a success, a permanent failure and
// a deliberate skip behave exactly as they did before.
//
// Why a park rather than a propagated error, which is what cqrs documents
// (docs/command-consumer.md:144: a transient failure "should return a normal
// error so the framework retries"): inside this chain a propagated error cannot
// work. cdb's result-sender wrapper converts ANY handler error into a published
// Failure result and returns nil
// (cqrs/cdb/cdb_command-object-executor-tx-result-sender.go:59), which is exactly
// the loss this change exists to fix — the batch commits and the offset advances
// past the command (kafka/kafka_consumer-offset.go:489-494), as it did for three
// release tasks on 2026-11-24. Letting the error past that wrapper means composing
// the chain by hand, and then the error rolls back the WHOLE batch: with
// COMMAND_BATCH_SIZE at 100 and RetryConsumer re-entering every 5 s, one flapping
// command replays up to 99 already-executed commands for the length of the outage
// — duplicate result messages and duplicate vault writes, including the
// delta-applying increment. Parking commits the batch and retries only the command
// that failed.
//
// The park is committed by the same transaction as the handler work, but the
// offset is written immediately AFTER that transaction commits
// (kafka_offset_store.go). A crash in that gap leaves the command parked AND the
// message re-readable, so it is executed twice at worst; a duplicate create is
// answered with ErrTaskAlreadyExists rather than a second file, and the redriver
// clears that case as decided.
//
// Only operations that are safe to repeat may be wrapped with this: a create is
// (an identifier that already exists is answered with ErrTaskAlreadyExists rather
// than a second file), a delta-applying increment is not.
func NewTransientAwareExecutorTx(
	inner cdb.CommandObjectExecutorTx,
	park CommandPark,
	currentDateTime libtime.CurrentDateTimeGetter,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		inner.CommandOperation(),
		inner.SendResultEnabled(),
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			resultEventID, resultEvent, resultErr := inner.HandleCommand(ctx, tx, commandObject)
			if resultErr == nil || errors.Is(resultErr, cdb.ErrCommandObjectSkipped) {
				return resultEventID, resultEvent, resultErr
			}
			if !vaultapi.IsTransient(resultErr) {
				return resultEventID, resultEvent, resultErr
			}

			taskIdentifier := commandIdentity(commandObject, "taskIdentifier")
			if err := park.Park(
				ctx,
				tx,
				commandObject.Command,
				currentDateTime.Now().Time(),
				taskIdentifier,
			); err != nil {
				// A command that could not be parked must not be reported as
				// skipped: that would commit the offset past it and lose it. The
				// error is propagated so the batch transaction rolls back and the
				// message is re-read — replaying the batch is the lesser harm when
				// the park store itself is broken. Logged at Error because this
				// branch is only reached when the controller is already losing
				// commands.
				glog.Errorf(
					"parking command '%s' for task=%s failed, rolling the batch back: %v",
					inner.CommandOperation(),
					taskIdentifier,
					err,
				)
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"park transiently failing command '%s'",
					inner.CommandOperation(),
				)
			}
			// Unconditional Warning, not V(n)-gated and not sampled: this is the
			// line that makes a command the controller could not execute
			// attributable from the log alone. The library's failure line
			// (cdb_command-object-executor-tx-result-sender.go:55-58) names the
			// operation and nothing else, which is why the 2026-11-24 losses could
			// not be traced to a task.
			glog.Warning(transientRetryMessage(inner.CommandOperation(), commandObject, resultErr))
			// Skipped, not nil: nil with SendResultEnabled would publish a Success
			// result for a command that has not executed yet.
			return nil, nil, cdb.ErrCommandObjectSkipped
		},
	)
}

// transientRetryMessage renders the log line for a command that has been parked
// for re-drive. It is a separate function so the one property that matters — the
// line names the task it could not execute — is pinned by a test rather than
// inferred from a format string.
func transientRetryMessage(
	commandOperation base.CommandOperation,
	commandObject cdb.CommandObject,
	err error,
) string {
	return fmt.Sprintf(
		"command '%s' failed transiently and is parked for re-drive: task=%s vault=%s request=%s error=%v",
		commandOperation,
		commandIdentity(commandObject, "taskIdentifier"),
		commandIdentity(commandObject, "targetVault"),
		commandObject.Command.RequestID,
		err,
	)
}

// commandIdentity returns one identifying field of the command payload for the
// log line, or "-" when the payload does not carry it. It reads the decoded
// event map rather than unmarshalling a per-operation type, because this wrapper
// sits in front of every operation and must not know their shapes.
func commandIdentity(commandObject cdb.CommandObject, key base.FieldName) string {
	value, ok := commandObject.Command.Data[key]
	if !ok || value == nil {
		return "-"
	}
	if s, ok := value.(string); ok {
		if s == "" {
			return "-"
		}
		return s
	}
	return fmt.Sprintf("%v", value)
}
