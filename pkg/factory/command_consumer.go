// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

// newCommandOffsetManager returns the offset manager the command consumer starts from. It is
// NewTTLSeekOffsetManager rather than the NewStoreOffsetManager that cdb.RunCommandConsumerTx
// hardcodes, because that one resolves a partition with no committed offset to OffsetOldest and
// replays the whole retained topic. The TTL-seek manager starts at the first offset at or after
// now minus commandExpireDuration — the oldest command that can still be executed — and reports
// OffsetNewest from both InitialOffset and FallbackOffset so no recovery path reinstates the
// replay.
func newCommandOffsetManager(
	saramaClient libkafka.SaramaClient,
	offsetStore libkafka.OffsetStore,
	currentDateTime libtime.CurrentDateTimeGetter,
) libkafka.OffsetManager {
	return libkafka.NewTTLSeekOffsetManager(
		saramaClient,
		offsetStore,
		libtime.Duration(commandExpireDuration),
		currentDateTime,
	)
}

// newCommandHandler places the expiry filter outside the batch handler, so an expired command is
// dropped before the batch reaches the transaction that cdb.CreateCommandMessageHandlerBatch opens
// for it. A batch whose commands have all expired — the shape every replayed batch takes — then
// opens no handler transaction at all (the single offset commit after the batch still writes), and
// a partly expired batch never carries the dead messages into one.
func newCommandHandler(
	handler libkafka.MessageHandlerBatch,
	currentDateTime libtime.CurrentDateTimeGetter,
) libkafka.MessageHandlerBatch {
	return libkafka.NewMessageHandlerBatchFilter(
		command.NewExpiredCommandSkip(commandExpireDuration, currentDateTime),
		handler,
	)
}

// newCommandConsumerFunc returns the run.Func that consumes the request topic, composed by hand
// rather than through cdb.RunCommandConsumerTx so the offset manager and the handler wrapper above
// can be supplied.
//
// The sarama client is obtained from the provider for the TTL seek only and is deliberately not
// closed here: the provider owns its clients and main.go closes the provider. With the reused
// provider this repository selects, the returned wrapper's Close is a documented no-op; closing it
// per attempt would add nothing, and a pooled provider would want it returned once per run rather
// than once per RetryConsumer attempt.
//
// The seek target is now minus commandExpireDuration in the broker's message-timestamp space,
// while the drop predicate reads the command's own requestTime. The two agree only while producer
// and consumer clocks agree: a producer running ahead can stamp a requestTime inside the window on
// a message appended before it, and the seek then skips a command the old oldest-first start would
// have executed. That is accepted — it applies only when the offset store is empty, and it is what
// removes the multi-day replay.
//
// batchSize is threaded through unchanged: it is the only lever over how many messages share one
// libkv.Tx, so hardcoding it here would silently undo the batch-tx change.
func newCommandConsumerFunc(
	saramaClientProvider libkafka.SaramaClientProvider,
	db libkv.DB,
	batchSize libkafka.BatchSize,
	topicPrefix base.TopicPrefix,
	handler libkafka.MessageHandlerBatch,
	currentDateTime libtime.CurrentDateTimeGetter,
) run.Func {
	return func(ctx context.Context) error {
		saramaClient, err := saramaClientProvider.Client(ctx)
		if err != nil {
			return errors.Wrap(ctx, err, "create sarama client failed")
		}
		return libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(
			saramaClientProvider,
			lib.TaskV1SchemaID.CommandTopic(topicPrefix),
			newCommandOffsetManager(saramaClient, libkafka.NewOffsetStore(db), currentDateTime),
			newCommandHandler(handler, currentDateTime),
			batchSize,
			run.NewTrigger(),
			log.DefaultSamplerFactory,
		).Consume(ctx)
	}
}
