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
func NewTaskPublisher(
	eventObjectSender cdb.EventObjectSender,
	schemaID cdb.SchemaID,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) TaskPublisher {
	return &taskPublisher{
		eventObjectSender:     eventObjectSender,
		schemaID:              schemaID,
		currentDateTimeGetter: currentDateTimeGetter,
	}
}

type taskPublisher struct {
	eventObjectSender     cdb.EventObjectSender
	schemaID              cdb.SchemaID
	currentDateTimeGetter libtime.CurrentDateTimeGetter
}

func (p *taskPublisher) PublishChanged(ctx context.Context, task lib.Task) error {
	now := p.currentDateTimeGetter.Now()
	task.Object = base.Object[base.Identifier]{
		Identifier: base.Identifier(uuid.New().String()),
		Created:    now,
		Modified:   now,
	}
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
