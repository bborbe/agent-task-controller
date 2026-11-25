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
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/agent-task-controller/pkg/command"
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

// CreateCommandConsumer wires a CQRS command consumer for agent-task-v1-request,
// together with the loop that re-drives the commands it had to park.
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
	vaultClient vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
	humans result.HumanAssignees,
	verifier command.WriteVerifier,
) (run.Func, run.Func) {
	park := command.NewCommandPark(db)
	executors, createTaskExecutor := newCommandExecutors(
		vaultClient,
		taskDir,
		vaultName,
		currentDateTime,
		k,
		resolver,
		verifier,
		resultWriter,
		prCommenter,
		m,
		humans,
		park,
	)
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
	consumer := RetryConsumer(
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
	// The re-drive loop shares the park and the create executor with the consumer:
	// it executes exactly the command the consumer parked, against the same vault
	// and the same identifier->path index.
	redriver := command.NewParkRedriver(
		db,
		park,
		createTaskExecutor,
		lib.TaskV1SchemaID,
		cdb.NewResultObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory),
		commandExpireDuration,
		command.DefaultParkRedriveDelay,
		command.DefaultParkRedriveInterval,
		currentDateTime,
		m,
	)
	return consumer, redriver.Run
}

// newCommandExecutors returns the executor set the command handler dispatches by
// operation, together with the raw create executor the park re-drive re-runs.
//
// Only the create executor is wrapped with the park: a create is safe to repeat
// (an identifier that already exists is answered with ErrTaskAlreadyExists
// instead of a second file), while the increment executor applies a delta and
// would double-count on a repeat.
func newCommandExecutors(
	vaultClient vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	resolver result.TaskPathResolver,
	verifier command.WriteVerifier,
	resultWriter result.ResultWriter,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
	humans result.HumanAssignees,
	park command.CommandPark,
) (cdb.CommandObjectExecutorTxs, cdb.CommandObjectExecutorTx) {
	retryGate := command.NewPlanningRetryGate(
		vaultClient,
		taskDir,
		vaultName,
		currentDateTime,
		prCommenter,
		m,
		humans,
	)
	createTaskExecutor := command.NewCreateTaskExecutor(
		vaultClient,
		taskDir,
		vaultName,
		currentDateTime,
		k,
		resolver,
		verifier,
	)
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter, retryGate, vaultName),
		// First executor ported to the typed surface: it addresses the task by
		// identifier and PATCHes the fields it changes, instead of reading a
		// vault-relative path, mutating the YAML and re-serialising the file.
		command.NewIncrementFrontmatterExecutor(vaultClient, vaultName, currentDateTime, m),
		command.NewUpdateFrontmatterExecutor(
			vaultClient,
			vaultName,
			currentDateTime,
			m,
			humans,
			verifier,
		),
		command.NewTransientAwareExecutorTx(createTaskExecutor, park, currentDateTime),
		command.NewCompleteTaskExecutor(vaultClient, vaultName, currentDateTime, m),
	}
	return executors, createTaskExecutor
}
