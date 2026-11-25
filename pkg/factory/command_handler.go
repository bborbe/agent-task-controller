// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// createCommandHandlerBatch composes the command handler chain by hand instead
// of calling cdb.CreateCommandMessageHandlerBatch, for one reason: that helper
// makes a handler failure indistinguishable from success.
//
// It wraps every executor in cdb.NewCommandObjectExecutorTxResultSender, which
// publishes a Failure result and returns nil for any handler error
// (cqrs/cdb/cdb_command-object-executor-tx-result-sender.go:66), and it wraps the
// chain in libkafka.NewMessageHandlerTxSkipErrors, which swallows anything that
// still escapes (kafka/kafka_message-handler-tx-skip.go:48). The batch
// transaction therefore commits, and the consumer marks the offset past the
// command (kafka/kafka_consumer-offset.go:498-505) — a create-task whose vault
// write timed out is lost. Measured 2026-11-24: three release tasks.
//
// The composition below is the same chain with the two behaviours the controller
// needs: command.NewTransientAwareExecutorTx publishes the same results but lets a
// transient vault error escape, and newSkipErrorsExceptTransient drops every other
// handler error exactly as before while passing that one through. The escaped
// error rolls the batch transaction back, so the offset stays put and the command
// is re-read (see RetryConsumer) until it succeeds or the 60-minute expiry filter
// drops it.
func createCommandHandlerBatch(
	db libkv.DB,
	syncProducer libkafka.SyncProducer,
	schemaID cdb.SchemaID,
	ignoreUnsupported bool,
	prefix base.TopicPrefix,
	commandExpireDuration time.Duration,
	commandObjectExecutors ...cdb.CommandObjectExecutorTx,
) libkafka.MessageHandlerBatch {
	resultObjectSender := cdb.NewResultObjectSender(
		syncProducer,
		prefix,
		log.DefaultSamplerFactory,
	)
	executors := make(cdb.CommandObjectExecutorTxs, 0, len(commandObjectExecutors))
	for _, commandObjectExecutor := range commandObjectExecutors {
		executors = append(
			executors,
			command.NewTransientAwareExecutorTx(
				cdb.NewCommandObjectExecutorTxMetrics(commandObjectExecutor, schemaID),
				resultObjectSender,
				log.DefaultSamplerFactory,
			),
		)
	}
	return libkafka.NewMessageHandlerBatchTxUpdate(
		db,
		libkafka.NewMessageHandlerBatchTx(
			newSkipErrorsExceptTransient(
				libkafka.NewMessageHandlerTxMetrics(
					cdb.NewCommandObjectMessageHandlerTx(
						schemaID,
						cdb.NewCommandObjectHandlerTx(ignoreUnsupported, executors...),
						commandExpireDuration,
					),
					libkafka.NewMetrics(),
				),
				log.DefaultSamplerFactory,
			),
		),
	)
}

// newSkipErrorsExceptTransient is libkafka.NewMessageHandlerTxSkipErrors with one
// change: a transient vault failure is returned instead of swallowed, so the
// consumer does not commit the offset past a command it has not executed.
//
// Everything else is unchanged — the error is logged at the same sampled Warning
// and the message is skipped, because a permanently failing command must not
// wedge the partition.
func newSkipErrorsExceptTransient(
	handler libkafka.MessageHandlerTx,
	logSamplerFactory log.SamplerFactory,
) libkafka.MessageHandlerTx {
	logSampler := logSamplerFactory.Sampler()
	return libkafka.MessageHandlerTxFunc(
		func(ctx context.Context, tx libkv.Tx, msg *sarama.ConsumerMessage) error {
			err := handler.ConsumeMessage(ctx, tx, msg)
			if err == nil {
				return nil
			}
			if vaultapi.IsTransient(err) {
				glog.Warningf(
					"consume message with offset %d in partition %d in topic %s failed transiently, not committing the offset: %v",
					msg.Offset,
					msg.Partition,
					msg.Topic,
					err,
				)
				return err
			}
			if logSampler.IsSample() {
				data := errors.DataFromError(
					errors.AddDataToError(
						err,
						map[string]any{
							"topic":     msg.Topic,
							"partition": msg.Partition,
							"offset":    msg.Offset,
						},
					),
				)
				glog.Warningf(
					"consume message with offset %d in partition %d in topic %s failed: %v %+v (sample)",
					msg.Offset,
					msg.Partition,
					msg.Topic,
					err,
					data,
				)
			}
			return nil
		},
	)
}
