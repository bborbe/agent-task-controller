// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/agent-task-controller/pkg/command"
	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
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
//
// batchSize bounds how many messages share one libkv.Tx. The handler composition
// (cdb.CreateCommandMessageHandlerBatch) is already MessageHandlerBatchTx — one
// db.Update per batch — so a batch size of 1 makes every message pay its own
// BoltDB transaction and fsync. Raising it is the only lever that changes the
// transaction count; the handler choice itself is already the batch variant.
func CreateCommandConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	syncProducer libkafka.SyncProducer,
	db libkv.DB,
	batchSize libkafka.BatchSize,
	topicPrefix base.TopicPrefix,
	resultWriter result.ResultWriter,
	resolver result.TaskPathResolver,
	gitClient gitclient.GitClient,
	vaultClient vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
	humans result.HumanAssignees,
) run.Func {
	retryGate := command.NewPlanningRetryGate(
		gitClient,
		taskDir,
		vaultName,
		currentDateTime,
		prCommenter,
		m,
		resolver,
		humans,
	)
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter, retryGate, vaultName),
		// First executor ported to the typed surface: it addresses the task by
		// identifier and PATCHes the fields it changes, instead of reading a
		// vault-relative path, mutating the YAML and re-serialising the file.
		command.NewIncrementFrontmatterExecutor(vaultClient, vaultName, currentDateTime, m),
		command.NewUpdateFrontmatterExecutor(vaultClient, vaultName, currentDateTime, m, humans),
		command.NewCreateTaskExecutor(
			gitClient,
			vaultClient,
			taskDir,
			vaultName,
			currentDateTime,
			k,
			resolver,
		),
		command.NewCompleteTaskExecutor(vaultClient, vaultName, currentDateTime, m),
	}
	// Composed by hand rather than through cdb.RunCommandConsumerTx; the reasons live with the
	// composition in newCommandConsumerFunc.
	handler := cdb.CreateCommandMessageHandlerBatch(
		db,
		syncProducer,
		lib.TaskV1SchemaID,
		true, // ignoreUnsupported: skip commands with unknown operations
		topicPrefix,
		commandExpireDuration,
		executors...,
	)
	return RetryConsumer(
		newCommandConsumerFunc(
			saramaClientProvider,
			db,
			batchSize,
			topicPrefix,
			handler,
			command.NewLoggingExpiredCommandObserver(m),
			currentDateTime,
		),
		retryConsumerDelay,
	)
}
