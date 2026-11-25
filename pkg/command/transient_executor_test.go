// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("transient-aware executor", func() {
	var ctx context.Context
	var schemaID cdb.SchemaID
	var commandObject cdb.CommandObject

	BeforeEach(func() {
		ctx = context.Background()
		schemaID = cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}
		commandObject = cdb.CommandObject{
			SchemaID: schemaID,
			Command: base.Command{
				RequestID: "1234567890",
				Operation: "create-task",
				Initiator: "me",
				Data: base.Event{
					"taskIdentifier": "abc12345",
					"targetVault":    "private-personal",
				},
			},
		}
	})

	newSender := func(sent *[]cdb.ResultObject) cdb.ResultObjectSender {
		return cdb.ResultObjectSenderFunc(
			func(_ context.Context, resultObject cdb.ResultObject) error {
				*sent = append(*sent, resultObject)
				return nil
			},
		)
	}

	newExecutor := func(handle cdb.HandleCommandFunc) cdb.CommandObjectExecutorTx {
		return cdb.CommandObjectExecutorTxFunc("create-task", true, handle)
	}

	It("lets a transient failure escape and publishes no result", func() {
		// A Failure result here would tell every result-topic consumer the command
		// is decided, while the consumer is about to retry it — and the library
		// wrapper would then report success, committing the offset past it.
		sent := make([]cdb.ResultObject, 0)
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Wrapf(
				ctx,
				vaultapi.ErrTransient,
				"create task: unexpected status 503",
			)
		})
		wrapped := command.NewTransientAwareExecutorTx(
			executor,
			newSender(&sent),
			log.DefaultSamplerFactory,
		)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeTrue())
		Expect(sent).To(BeEmpty())
	})

	It("publishes a Failure result and reports success for a permanent failure", func() {
		sent := make([]cdb.ResultObject, 0)
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Errorf(ctx, "validate task_identifier failed")
		})
		wrapped := command.NewTransientAwareExecutorTx(
			executor,
			newSender(&sent),
			log.DefaultSamplerFactory,
		)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(BeNil())
		Expect(sent).To(HaveLen(1))
		Expect(sent[0].Result.Success).To(BeFalse())
		Expect(sent[0].Result.Message).To(ContainSubstring("validate task_identifier"))
	})

	It("publishes nothing for a skipped command", func() {
		sent := make([]cdb.ResultObject, 0)
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Wrapf(ctx, cdb.ErrCommandObjectSkipped, "cross-vault")
		})
		wrapped := command.NewTransientAwareExecutorTx(
			executor,
			newSender(&sent),
			log.DefaultSamplerFactory,
		)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(BeNil())
		Expect(sent).To(BeEmpty())
	})

	It("publishes a Success result when the command succeeds", func() {
		sent := make([]cdb.ResultObject, 0)
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, nil
		})
		wrapped := command.NewTransientAwareExecutorTx(
			executor,
			newSender(&sent),
			log.DefaultSamplerFactory,
		)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(BeNil())
		Expect(sent).To(HaveLen(1))
		Expect(sent[0].Result.Success).To(BeTrue())
	})

	It("names the task in the retry log line", func() {
		// The failure that motivated this wrapper (2026-11-24) could not be traced
		// to a task from the log: the library line carries the operation only.
		line := command.TransientRetryMessage(
			"create-task",
			commandObject,
			errors.Errorf(ctx, "POST /api/v1/tasks: context deadline exceeded"),
		)
		Expect(line).To(ContainSubstring("task=abc12345"))
		Expect(line).To(ContainSubstring("vault=private-personal"))
		Expect(line).To(ContainSubstring("create-task"))
	})

	It("falls back to a dash when the payload carries no task identity", func() {
		line := command.TransientRetryMessage(
			"update-frontmatter",
			cdb.CommandObject{
				SchemaID: schemaID,
				Command:  base.Command{RequestID: "1234567890", Operation: "update-frontmatter"},
			},
			errors.Errorf(ctx, "boom"),
		)
		Expect(line).To(ContainSubstring("task=-"))
	})
})
