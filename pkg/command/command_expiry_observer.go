// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	libkafka "github.com/bborbe/kafka"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// ExpiredCommandObserver is told about every command the expiry predicate drops.
//
// It exists because a dropped command is otherwise invisible: the predicate reports a bool, so
// nothing downstream can name what was lost. An observer turns the drop into a record carrying
// the command's own identifiers, which is what makes "a published create never reached the
// controller" answerable from the controller's own logs and metrics.
type ExpiredCommandObserver interface {
	ObserveExpired(ctx context.Context, command base.Command, msg *sarama.ConsumerMessage)
}

// NewExpiredCommandBatchHandler returns a batch handler that drops the messages skip reports and
// reports each of them to observer, then passes the remaining messages, in their original order,
// to handler in a single call.
//
// It replaces libkafka.NewMessageHandlerBatchFilter rather than wrapping it, because that filter
// takes only the predicate and the handler and therefore has nowhere to report a drop. The
// partition-and-forward behaviour is otherwise identical, including the context check and the
// early return when every message in the batch was dropped.
//
// skip must keep the contract libkafka states for it — pure, free of side effects, no state
// carried across calls — because the returned handler is shared by one consumer goroutine per
// partition. The observation deliberately does NOT live inside skip: a side effect there would
// run on every message of every partition concurrently and would couple the predicate's verdict
// to whatever the observer does. The wrapper observes after the verdict, per dropped message.
//
// A dropped message is JSON-decoded a second time here, to hand the observer the command rather
// than only the raw bytes. That decode is paid only for dropped messages, which are rare, and it
// cannot fail in practice: skip returns true only after it decoded the same bytes successfully.
// The failure branch is kept so that a future predicate with different rules cannot silently
// swallow the observation.
func NewExpiredCommandBatchHandler(
	skip func(*sarama.ConsumerMessage) bool,
	observer ExpiredCommandObserver,
	handler libkafka.MessageHandlerBatch,
) libkafka.MessageHandlerBatch {
	return libkafka.MessageHandlerBatchFunc(
		func(ctx context.Context, messages []*sarama.ConsumerMessage) error {
			kept := make([]*sarama.ConsumerMessage, 0, len(messages))
			for _, msg := range messages {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				if !skip(msg) {
					kept = append(kept, msg)
					continue
				}
				observer.ObserveExpired(ctx, decodeDroppedCommand(ctx, msg), msg)
			}
			if len(kept) == 0 {
				return nil
			}
			return handler.ConsumeMessages(ctx, kept)
		},
	)
}

// decodeDroppedCommand returns the command carried by a message the predicate dropped. A decode
// failure is logged with the message coordinates and yields a zero command, so the observer still
// records the drop — losing the observation would be worse than losing its labels.
func decodeDroppedCommand(ctx context.Context, msg *sarama.ConsumerMessage) base.Command {
	var command base.Command
	if err := json.Unmarshal(msg.Value, &command); err != nil {
		glog.Warningf(
			"decode dropped command failed, observing with empty command: topic=%s partition=%d offset=%d error=%v",
			msg.Topic,
			msg.Partition,
			msg.Offset,
			err,
		)
	}
	return command
}

// LoggingExpiredCommandObserver logs every dropped command unconditionally and counts it.
//
// The log is unconditional rather than V(n)-gated: a dropped command is the event this whole
// change exists to expose, and a gated line is exactly what made the drop unobservable before —
// the library logged it at Warning only when its sampler fired, and never named the command.
//
// It deliberately writes no result object. An expired command is usually a create-task whose
// task file does not exist yet, so a result write would land as not_found noise on the
// ResultsWrittenTotal counter that AgentControllerResultNotFound alerts on. The log line and the
// counter are the record; making the command loud is this change's scope, not re-routing it.
type LoggingExpiredCommandObserver struct {
	metrics metrics.Metrics
}

// NewLoggingExpiredCommandObserver returns an ExpiredCommandObserver that logs and counts drops.
func NewLoggingExpiredCommandObserver(m metrics.Metrics) ExpiredCommandObserver {
	return &LoggingExpiredCommandObserver{metrics: m}
}

// ObserveExpired logs the dropped command and increments CommandExpiredTotal for its operation.
func (o *LoggingExpiredCommandObserver) ObserveExpired(
	_ context.Context,
	command base.Command,
	msg *sarama.ConsumerMessage,
) {
	glog.Warningf(
		"command expired and dropped: requestID=%s operation=%s requestTime=%s topic=%s partition=%d offset=%d",
		command.RequestID,
		command.Operation,
		command.RequestTime.Format(time.RFC3339),
		msg.Topic,
		msg.Partition,
		msg.Offset,
	)
	o.metrics.CommandExpiredTotal(string(command.Operation)).Inc()
}
