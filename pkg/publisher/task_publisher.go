// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package publisher

import (
	"context"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/google/uuid"
)

//counterfeiter:generate -o ../../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher publishes task change and deletion events to Kafka.
type TaskPublisher interface {
	// PublishChanged publishes an upsert event for the given task.
	PublishChanged(ctx context.Context, task lib.Task) error
	// PublishDeleted publishes a deletion event for the given task identifier.
	PublishDeleted(ctx context.Context, id lib.TaskIdentifier) error
}

// NewTaskPublisher creates a TaskPublisher that sends events via EventObjectSender.
// Every changed event is stamped with vaultName in Task.Vault; consumers that
// share one topic across vaults (agent-task-executor) filter on it.
// frontmatter["target_vault"] remains the routing field for results.
func NewTaskPublisher(
	eventObjectSender cdb.EventObjectSender,
	schemaID cdb.SchemaID,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	vaultName string,
) TaskPublisher {
	return &taskPublisher{
		eventObjectSender:     eventObjectSender,
		schemaID:              schemaID,
		currentDateTimeGetter: currentDateTimeGetter,
		vaultName:             vaultName,
	}
}

type taskPublisher struct {
	eventObjectSender     cdb.EventObjectSender
	schemaID              cdb.SchemaID
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	vaultName             string
}

// PublishChanged publishes an upsert event for the given task. The event is
// always stamped with the controller's configured vault in Task.Vault,
// overriding any value carried by the input task.
func (p *taskPublisher) PublishChanged(ctx context.Context, task lib.Task) error {
	now := p.currentDateTimeGetter.Now()
	task.Object = base.Object[base.Identifier]{
		Identifier: base.Identifier(uuid.New().String()),
		Created:    now,
		Modified:   now,
	}
	task.Vault = p.vaultName
	event, err := base.ParseEvent(ctx, task)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse event for task %s failed", task.TaskIdentifier)
	}
	if err := p.eventObjectSender.SendUpdate(ctx, cdb.EventObject{
		Event:    event,
		ID:       base.EventID(task.TaskIdentifier),
		SchemaID: p.schemaID,
	}); err != nil {
		return errors.Wrapf(ctx, err, "publish changed task %s failed", task.TaskIdentifier)
	}
	return nil
}

func (p *taskPublisher) PublishDeleted(ctx context.Context, id lib.TaskIdentifier) error {
	if err := p.eventObjectSender.SendDelete(ctx, cdb.EventObject{
		ID:       base.EventID(id),
		SchemaID: p.schemaID,
	}); err != nil {
		return errors.Wrapf(ctx, err, "publish deleted task %s failed", id)
	}
	return nil
}
