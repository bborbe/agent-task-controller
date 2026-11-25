// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	kafkamocks "github.com/bborbe/kafka/mocks"
	libkv "github.com/bborbe/kv"
	kvmocks "github.com/bborbe/kv/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// This pins the central claim of the park design against the REAL library chain,
// not against a fake: cdb.CreateCommandMessageHandlerBatch must return nil for a
// transiently failing command (so the batch transaction commits and the consumer
// marks the offset) while publishing NO result, and the command must be parked.
//
// The failure it guards against is the one that lost three release tasks on
// 2026-11-24: a transient error became a Failure result plus a committed offset,
// and the command was never re-read.
var _ = Describe("command handler batch", func() {
	const expiryWindow = 60 * time.Minute

	var ctx context.Context
	var db *kvmocks.DB
	var producer *kafkamocks.KafkaSyncProducer
	var park *mocks.CommandPark
	var messages []*sarama.ConsumerMessage
	var currentDateTime libtime.CurrentDateTimeGetter
	var now time.Time

	BeforeEach(func() {
		ctx = context.Background()
		db = &kvmocks.DB{}
		db.UpdateStub = func(
			ctx context.Context,
			fn func(ctx context.Context, tx libkv.Tx) error,
		) error {
			return fn(ctx, &kvmocks.Tx{})
		}
		producer = &kafkamocks.KafkaSyncProducer{}
		park = &mocks.CommandPark{}
		now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
		currentDateTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})

		value, err := json.Marshal(base.Command{
			RequestID:   "1234567890",
			RequestTime: time.Now(),
			Initiator:   "me",
			Operation:   "create-task",
			Data: base.Event{
				"taskIdentifier": "abc12345",
				"targetVault":    "private-personal",
			},
		})
		Expect(err).To(Succeed())
		messages = []*sarama.ConsumerMessage{
			{Topic: "commands", Partition: 0, Offset: 780982, Value: value},
		}
	})

	newHandler := func(executor cdb.CommandObjectExecutorTx) libkafka.MessageHandlerBatch {
		return cdb.CreateCommandMessageHandlerBatch(
			db,
			producer,
			lib.TaskV1SchemaID,
			true,
			base.TopicPrefix("myprefix"),
			expiryWindow,
			command.NewTransientAwareExecutorTx(executor, park, currentDateTime),
		)
	}

	It("commits the batch without publishing a result when the command fails transiently", func() {
		handler := newHandler(
			cdb.CommandObjectExecutorTxFunc(
				"create-task",
				true,
				func(
					_ context.Context,
					_ libkv.Tx,
					_ cdb.CommandObject,
				) (*base.EventID, base.Event, error) {
					return nil, nil, errors.Wrapf(
						ctx,
						vaultapi.ErrTransient,
						"create task: unexpected status 503",
					)
				},
			),
		)

		// nil return = the batch transaction commits and the consumer marks the
		// offset: the command is no longer re-readable, which is why the park has to
		// exist before this returns.
		Expect(handler.ConsumeMessages(ctx, messages)).To(Succeed())
		Expect(park.ParkCallCount()).To(Equal(1))
		_, _, parkedCommand, _, taskIdentifier := park.ParkArgsForCall(0)
		Expect(parkedCommand.RequestID).To(Equal(base.RequestID("1234567890")))
		Expect(taskIdentifier).To(Equal("abc12345"))
		// No Success and no Failure: the command is not decided yet.
		Expect(producer.SendMessageCallCount()).To(Equal(0))
	})

	It("publishes a Failure result and commits for a permanent error", func() {
		handler := newHandler(
			cdb.CommandObjectExecutorTxFunc(
				"create-task",
				true,
				func(
					_ context.Context,
					_ libkv.Tx,
					_ cdb.CommandObject,
				) (*base.EventID, base.Event, error) {
					return nil, nil, errors.Errorf(ctx, "validate task_identifier failed")
				},
			),
		)

		Expect(handler.ConsumeMessages(ctx, messages)).To(Succeed())
		Expect(park.ParkCallCount()).To(Equal(0))
		Expect(producer.SendMessageCallCount()).To(Equal(1))
	})
})
