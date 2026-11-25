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

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// fakePark records what the executor asked it to park.
type fakePark struct {
	parked []base.Command
	tasks  []string
	at     []time.Time
	err    error
}

func (f *fakePark) Park(
	_ context.Context,
	_ libkv.Tx,
	command base.Command,
	parkedAt time.Time,
	taskIdentifier string,
) error {
	if f.err != nil {
		return f.err
	}
	f.parked = append(f.parked, command)
	f.tasks = append(f.tasks, taskIdentifier)
	f.at = append(f.at, parkedAt)
	return nil
}

func (f *fakePark) List(context.Context) ([]command.ParkedCommand, error) { return nil, nil }

func (f *fakePark) Delete(context.Context, string) error { return nil }

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
		park := &fakePark{}
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
		Expect(park.parked).To(HaveLen(1))
		Expect(park.parked[0].RequestID).To(Equal(base.RequestID("1234567890")))
		Expect(park.tasks[0]).To(Equal("abc12345"))
		Expect(park.at[0].Equal(now)).To(BeTrue())
	})

	It("propagates the error instead of skipping when the park itself fails", func() {
		// A command that could not be parked would be lost if it were reported as
		// skipped, so the failure must reach the consumer and roll the batch back.
		park := &fakePark{err: errors.Errorf(ctx, "bolt is broken")}
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
		Expect(park.parked).To(BeEmpty())
	})

	It("passes a permanent failure through untouched", func() {
		park := &fakePark{}
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
		Expect(park.parked).To(BeEmpty())
	})

	It("passes a deliberate skip through untouched", func() {
		park := &fakePark{}
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
		Expect(park.parked).To(BeEmpty())
	})

	It("passes success through untouched", func() {
		park := &fakePark{}
		executor := newExecutor(func(
			_ context.Context,
			_ libkv.Tx,
			_ cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return nil, nil, nil
		})
		wrapped := command.NewTransientAwareExecutorTx(executor, park, currentDateTime)

		_, _, err := wrapped.HandleCommand(ctx, nil, commandObject)
		Expect(err).To(BeNil())
		Expect(park.parked).To(BeEmpty())
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
