// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"time"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("transient-aware executor", func() {
	var ctx context.Context
	var schemaID cdb.SchemaID
	var commandObject cdb.CommandObject
	var now time.Time
	var currentDateTime libtime.CurrentDateTimeGetter

	BeforeEach(func() {
		ctx = context.Background()
		schemaID = cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}
		now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
		currentDateTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
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

	newExecutor := func(handle cdb.HandleCommandFunc) cdb.CommandObjectExecutorTx {
		return cdb.CommandObjectExecutorTxFunc("create-task", true, handle)
	}

	It("parks a transiently failing command and reports it as skipped", func() {
		// Skipped, not nil: a nil return with SendResultEnabled makes cdb's wrapper
		// publish a Success result for a command that has not executed.
		park := &mocks.CommandPark{}
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
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(park.ParkCallCount()).To(Equal(1))
		_, _, parkedCommand, parkedAt, taskIdentifier := park.ParkArgsForCall(0)
		Expect(parkedCommand.RequestID).To(Equal(base.RequestID("1234567890")))
		Expect(taskIdentifier).To(Equal("abc12345"))
		Expect(parkedAt.Time()).To(BeTemporally("==", now))
	})

	It("propagates the error instead of skipping when the park itself fails", func() {
		// A command that could not be parked would be lost if it were reported as
		// skipped, so the failure must reach the consumer and roll the batch back.
		park := &mocks.CommandPark{}
		park.ParkReturns(errors.Errorf(ctx, "bolt is broken"))
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Wrapf(ctx, vaultapi.ErrTransient, "timeout")
		})
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
		// It tried, and the failure is the park's — not a decision to skip.
		Expect(park.ParkCallCount()).To(Equal(1))
	})

	It("passes a permanent failure through untouched", func() {
		park := &mocks.CommandPark{}
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Errorf(ctx, "validate task_identifier failed")
		})
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
		Expect(park.ParkCallCount()).To(Equal(0))
	})

	It("passes a deliberate skip through untouched", func() {
		park := &mocks.CommandPark{}
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, errors.Wrapf(ctx, cdb.ErrCommandObjectSkipped, "cross-vault")
		})
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(park.ParkCallCount()).To(Equal(0))
	})

	It("passes success through untouched", func() {
		park := &mocks.CommandPark{}
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, nil
		})
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(Succeed())
		Expect(park.ParkCallCount()).To(Equal(0))
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

	It("renders an absent identity as a dash rather than an empty value", func() {
		line := command.TransientRetryMessage(
			"update-frontmatter",
			cdb.CommandObject{
				SchemaID: schemaID,
				Command: base.Command{
					RequestID: "1234567890",
					Operation: "update-frontmatter",
					Data:      base.Event{"taskIdentifier": ""},
				},
			},
			errors.Errorf(ctx, "boom"),
		)
		Expect(line).To(ContainSubstring("task=-"))
	})
})
