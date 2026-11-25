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
	"github.com/bborbe/log"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// NewTransientAwareExecutorTx wraps one command executor so a TRANSIENT vault
// failure is retried instead of being committed past.
//
// It replaces cdb.NewCommandObjectExecutorTxResultSender for this controller,
// because that wrapper makes every handler error look like success: it publishes
// a Failure result and returns nil, the batch handler therefore commits its
// transaction, and the consumer marks the offset past the command
// (cqrs/cdb/cdb_command-object-executor-tx-result-sender.go:66). A command whose
// vault write timed out is then gone for good — measured 2026-11-24, three
// release tasks lost between 17:44Z and 17:51Z. cqrs' own contract
// (docs/command-consumer.md:144) says a transient failure "should return a
// normal error so the framework retries"; nothing in the framework does.
//
// A transient error therefore escapes this wrapper (no result is published, the
// batch transaction rolls back, the offset is not advanced, and the consumer
// re-reads the same command). The command is retried until it succeeds or until
// the controller's 60-minute expiry filter drops it — and that drop is visible
// (Warning + CommandExpiredTotal), unlike the silent advance it replaces.
//
// Every other error keeps the library's behaviour exactly: ErrCommandObjectSkipped
// publishes nothing and returns nil, and any other error publishes a Failure
// result and returns nil.
func NewTransientAwareExecutorTx(
	inner cdb.CommandObjectExecutorTx,
	resultObjectSender cdb.ResultObjectSender,
	logSamplerFactory log.SamplerFactory,
) cdb.CommandObjectExecutorTx {
	commandOperation := inner.CommandOperation()
	resultEnabled := inner.SendResultEnabled()
	logSamplerFailure := logSamplerFactory.Sampler()
	logSamplerSend := logSamplerFactory.Sampler()
	return cdb.CommandObjectExecutorTxFunc(
		commandOperation,
		resultEnabled,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			resultEventID, resultEvent, resultErr := inner.HandleCommand(ctx, tx, commandObject)

			if resultErr != nil && errors.Is(resultErr, cdb.CommandObjectSkippedError) {
				glog.V(3).Infof("result returned skipped error => skip")
				return resultEventID, resultEvent, nil
			}

			// A transient failure is not a result. Publishing a Failure here would
			// make the command look decided to every consumer of the result topic,
			// while the command itself is about to be retried.
			if vaultapi.IsTransient(resultErr) {
				// Unconditional Warning, not V(n)-gated and not sampled: this is
				// the line that makes a command the controller could not execute
				// attributable from the log alone. The library's failure line
				// (cdb_command-object-executor-tx-result-sender.go:52) names the
				// operation and nothing else, which is why the 2026-11-24 losses
				// could not be traced to a task.
				glog.Warningf(
					"%s",
					transientRetryMessage(commandOperation, commandObject, resultErr),
				)
				return nil, nil, resultErr
			}

			if resultErr == nil && !resultEnabled {
				glog.V(3).Infof("result returned no error and resultEnabled is false => skip")
				return resultEventID, resultEvent, nil
			}

			var resultObject cdb.ResultObject
			if resultErr != nil {
				if logSamplerFailure.IsSample() {
					glog.V(2).Infof(
						"handler command '%s' failed with error: %v (task=%s vault=%s)",
						commandOperation,
						resultErr,
						commandIdentity(commandObject, "taskIdentifier"),
						commandIdentity(commandObject, "targetVault"),
					)
				}
				resultObject = cdb.CreateResultObjectFailure(commandObject, resultErr)
			} else {
				glog.V(3).Infof(
					"handler command '%s' successful with id '%s' and data %+v",
					commandOperation,
					resultEventID,
					resultEvent,
				)
				resultObject = cdb.CreateResultObjectSuccess(commandObject, resultEventID, resultEvent)
			}
			if err := resultObjectSender.Send(ctx, resultObject); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "send result failed")
			}
			if logSamplerSend.IsSample() {
				glog.V(3).Infof(
					"send result for request '%s' '%s' completed with success %v message: '%s' (sample)",
					commandOperation,
					commandObject.Command.RequestID,
					resultObject.Result.Success,
					resultObject.Result.Message,
				)
			}
			return resultEventID, resultEvent, nil
		},
	)
}

// transientRetryMessage renders the log line for a command whose execution is
// about to be retried. It is a separate function so the one property that
// matters — the line names the task it could not execute — is pinned by a test
// rather than inferred from a format string.
func transientRetryMessage(
	commandOperation base.CommandOperation,
	commandObject cdb.CommandObject,
	err error,
) string {
	return fmt.Sprintf(
		"command '%s' failed transiently and will be retried: task=%s vault=%s request=%s error=%v",
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
	if !ok {
		return "-"
	}
	if s, ok := value.(string); ok && s != "" {
		return s
	}
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%v", value)
}
