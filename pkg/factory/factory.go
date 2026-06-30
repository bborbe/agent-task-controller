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
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// CreateCommandConsumer wires a CQRS command consumer for agent-task-v1-request.
func CreateCommandConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	syncProducer libkafka.SyncProducer,
	db libkv.DB,
	branch base.Branch,
	resultWriter result.ResultWriter,
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
) run.Func {
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter),
		command.NewIncrementFrontmatterExecutor(gitClient, taskDir),
		command.NewUpdateFrontmatterExecutor(gitClient, taskDir),
		command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime),
	}
	return cdb.RunCommandConsumerTxDefault(
		saramaClientProvider,
		syncProducer,
		db,
		lib.TaskV1SchemaID,
		branch,
		true, // ignoreUnsupported: skip commands with unknown operations
		executors,
	)
}
