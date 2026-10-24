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

// newCommandConsumerFunc returns the run.Func that consumes the request topic. It is composed by
// hand rather than through cdb.RunCommandConsumerTx for two reasons:
//
//   - the start offset. RunCommandConsumerTx hardcodes NewStoreOffsetManager with OffsetOldest as
//     the initial offset, so a process with no committed offset replays the whole retained topic.
//     NewTTLSeekOffsetManager instead starts at the first offset at or after now minus
//     commandExpireDuration, which is the oldest command that can still be executed.
//   - the expiry check. cdb.CreateCommandMessageHandlerBatch decides expiry inside the transaction
//     that NewMessageHandlerBatchTxUpdate opens for every batch. NewMessageHandlerBatchFilter
//     composed outside it drops an expired command before the transaction exists.
func newCommandConsumerFunc(
	saramaClientProvider libkafka.SaramaClientProvider,
	db libkv.DB,
	topicPrefix base.TopicPrefix,
	handler libkafka.MessageHandlerBatch,
	currentDateTime libtime.CurrentDateTimeGetter,
) run.Func {
	return func(ctx context.Context) error {
		saramaClient, err := saramaClientProvider.Client(ctx)
		if err != nil {
			return errors.Wrap(ctx, err, "create sarama client failed")
		}
		defer func() {
			_ = saramaClient.Close()
		}()
		return libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(
			saramaClientProvider,
			lib.TaskV1SchemaID.CommandTopic(topicPrefix),
			libkafka.NewTTLSeekOffsetManager(
				saramaClient,
				libkafka.NewOffsetStore(db),
				libtime.Duration(commandExpireDuration),
				currentDateTime,
			),
			libkafka.NewMessageHandlerBatchFilter(
				command.NewExpiredCommandSkip(commandExpireDuration, currentDateTime),
				handler,
			),
			libkafka.BatchSize(1),
			run.NewTrigger(),
			log.DefaultSamplerFactory,
		).Consume(ctx)
	}
}
