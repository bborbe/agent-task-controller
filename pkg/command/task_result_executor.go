// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/routing"
)

// TaskResultCommandOperation is the CQRS command operation name for task result updates.
const TaskResultCommandOperation base.CommandOperation = "update"

// NewTaskResultExecutor creates a cdb.CommandObjectExecutorTx for update commands.
// Uses cdb.CommandObjectExecutorTxFunc adapter — same pattern as trading command handlers.
// vaultName is the controller's VAULT_NAME; a result whose target_vault frontmatter
// (stamped at task create, echoed by the agent) does not match is cross-vault traffic
// and is skipped before the write scan (no write, no not_found count, no result event).
func NewTaskResultExecutor(
	writer result.ResultWriter,
	retryGate PlanningRetryGate,
	vaultName string,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		TaskResultCommandOperation,
		true,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			var req lib.Task
			if err := commandObject.Command.Data.MarshalInto(ctx, &req); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"malformed Task command: %v",
					err,
				)
			}
			glog.V(2).
				Infof("task result executor: deserialized task %s (content length=%d, frontmatter keys=%d)", req.TaskIdentifier, len(req.Content), len(req.Frontmatter))
			if err := req.Validate(ctx); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"invalid Task (taskID=%s): %v",
					req.TaskIdentifier,
					err,
				)
			}
			if !routing.ShouldProcessResult(req, vaultName) {
				glog.V(2).Infof(
					"task result executor: skipped vault mismatch target_vault=%q vault=%q task=%s",
					req.Frontmatter["target_vault"], vaultName, req.TaskIdentifier,
				)
				// ErrCommandObjectSkipped — not nil: a nil return with SendResultEnabled
				// publishes a spurious Success result on the shared result topic for every
				// cross-vault result (go-cqrs/skipped-not-nil-for-non-retryable).
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"cross-vault result for task %s",
					req.TaskIdentifier,
				)
			}
			handled, err := retryGate.Handle(ctx, req)
			if err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"planning retry gate for task %s",
					req.TaskIdentifier,
				)
			}
			if handled {
				return resultEvent(ctx, req)
			}
			if err := writer.WriteResult(ctx, req); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"write result for task %s",
					req.TaskIdentifier,
				)
			}
			return resultEvent(ctx, req)
		},
	)
}

// resultEvent builds the result event and its eventID for a handled task.
// Used on both the retry-gate-handled and post-write paths.
func resultEvent(ctx context.Context, req lib.Task) (*base.EventID, base.Event, error) {
	event, err := base.ParseEvent(ctx, req)
	if err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			err,
			"parse result event for task %s",
			req.TaskIdentifier,
		)
	}
	eventID := base.EventID(req.TaskIdentifier)
	return eventID.Ptr(), event, nil
}
