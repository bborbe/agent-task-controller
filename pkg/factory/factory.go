// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/agent-task-controller/pkg/command"
	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// commandExpireDuration is the maximum age of a queued agent-task-v1-request
// command before it is dropped as expired. Sized at 60 minutes to absorb the
// worst-case queue residency of a 100+ task enqueue against a cap of 1
// concurrent job (measured job runtimes 11-15 min): a burst of agent
// completions, a slow git-rest round-trip, or a controller restart must not
// push tail commands past the window and silently drop their frontmatter
// writes. Stale-command protection is unchanged — a command older than 60
// minutes still expires rather than clobbering newer state.
const commandExpireDuration = 60 * time.Minute

// CreateCommandConsumer wires a CQRS command consumer for agent-task-v1-request.
func CreateCommandConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	syncProducer libkafka.SyncProducer,
	db libkv.DB,
	topicPrefix base.TopicPrefix,
	resultWriter result.ResultWriter,
	resolver result.TaskPathResolver,
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
) run.Func {
	retryGate := command.NewPlanningRetryGate(
		gitClient,
		taskDir,
		vaultName,
		currentDateTime,
		prCommenter,
		m,
		resolver,
	)
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter, retryGate, vaultName),
		command.NewIncrementFrontmatterExecutor(gitClient, taskDir, vaultName, m, resolver),
		command.NewUpdateFrontmatterExecutor(gitClient, taskDir, vaultName, m, resolver),
		command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k),
		command.NewCompleteTaskExecutor(
			gitClient,
			taskDir,
			vaultName,
			currentDateTime,
			m,
			resolver,
		),
	}
	// Composed by hand rather than through cdb.RunCommandConsumerTx for two reasons:
	//
	//   - the start offset. RunCommandConsumerTx hardcodes NewStoreOffsetManager with
	//     OffsetOldest as the initial offset, so a process with no committed offset replays the
	//     whole retained topic. NewTTLSeekOffsetManager instead starts at the first offset at or
	//     after now minus commandExpireDuration, which is the oldest command that can still be
	//     executed.
	//   - the expiry check. cdb.CreateCommandMessageHandlerBatch decides expiry inside the
	//     transaction that NewMessageHandlerBatchTxUpdate opens for every batch.
	//     NewMessageHandlerBatchFilter composed outside it drops an expired command before the
	//     transaction exists.
	handler := cdb.CreateCommandMessageHandlerBatch(
		db,
		syncProducer,
		lib.TaskV1SchemaID,
		true, // ignoreUnsupported: skip commands with unknown operations
		topicPrefix,
		commandExpireDuration,
		executors...,
	)
	consumer := func(ctx context.Context) error {
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
	return RetryConsumer(consumer, retryConsumerDelay)
}
