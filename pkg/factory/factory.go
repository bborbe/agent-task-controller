// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
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
)

// CreateCommandConsumer wires a CQRS command consumer for agent-task-v1-request.
func CreateCommandConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	syncProducer libkafka.SyncProducer,
	db libkv.DB,
	topicPrefix base.TopicPrefix,
	resultWriter result.ResultWriter,
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
) run.Func {
	retryGate := command.NewPlanningRetryGate(gitClient, taskDir, currentDateTime, prCommenter, m)
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter, retryGate),
		command.NewIncrementFrontmatterExecutor(gitClient, taskDir, m),
		command.NewUpdateFrontmatterExecutor(gitClient, taskDir, m),
		command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k),
	}
	return cdb.RunCommandConsumerTxDefault(
		saramaClientProvider,
		syncProducer,
		db,
		lib.TaskV1SchemaID,
		topicPrefix,
		true, // ignoreUnsupported: skip commands with unknown operations
		executors,
	)
}
