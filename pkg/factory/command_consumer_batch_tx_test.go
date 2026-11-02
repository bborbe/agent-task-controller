// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/IBM/sarama"
	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libkafka "github.com/bborbe/kafka"
	kafkamocks "github.com/bborbe/kafka/mocks"
	libkv "github.com/bborbe/kv"
	kvmocks "github.com/bborbe/kv/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

// This suite pins the property the controller depends on: the command handler
// composition is already the batch-tx variant, so the transaction count is
// driven by the batch size, not by the message count.
var _ = Describe("command consumer batch transaction", func() {
	const expiryWindow = 60 * time.Minute // must mirror pkg/factory commandExpireDuration

	var (
		ctx          context.Context
		db           *kvmocks.DB
		messageBatch libkafka.MessageHandlerBatch
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = &kvmocks.DB{}
		db.UpdateCalls(
			func(ctx context.Context, fn func(ctx context.Context, tx libkv.Tx) error) error {
				return fn(ctx, &kvmocks.Tx{})
			},
		)
		messageBatch = cdb.CreateCommandMessageHandlerBatch(
			db,
			&kafkamocks.KafkaSyncProducer{},
			lib.TaskV1SchemaID,
			true,
			base.TopicPrefix(""),
			expiryWindow,
		)
	})

	expiredMessage := func() *sarama.ConsumerMessage {
		value, err := json.Marshal(base.Command{
			RequestID:   "1234567890",
			RequestTime: time.Now().Add(-expiryWindow - time.Minute),
			Initiator:   "me",
			Operation:   "any-op",
		})
		Expect(err).To(BeNil())
		return &sarama.ConsumerMessage{Value: value}
	}

	It("opens one transaction for a batch of expired messages", func() {
		messages := make([]*sarama.ConsumerMessage, 100)
		for i := range messages {
			messages[i] = expiredMessage()
		}

		err := messageBatch.ConsumeMessages(ctx, messages)
		Expect(err).To(BeNil())
		Expect(db.UpdateCallCount()).To(Equal(1))
	})

	It("keeps skip semantics: a malformed message does not abort the batch", func() {
		messages := []*sarama.ConsumerMessage{
			{Value: []byte("not-json")},
			expiredMessage(),
		}

		err := messageBatch.ConsumeMessages(ctx, messages)
		Expect(err).To(BeNil())
		Expect(db.UpdateCallCount()).To(Equal(1))
	})

	It("propagates a transaction failure to the consumer", func() {
		txErr := stderrors.New("commit failed")
		db.UpdateReturns(txErr)

		err := messageBatch.ConsumeMessages(ctx, []*sarama.ConsumerMessage{expiredMessage()})
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(txErr))
		Expect(db.UpdateCallCount()).To(Equal(1))
	})

	// The consumer composes the expiry filter outside the batch handler, so an all-expired
	// batch — the shape every replayed batch takes — never reaches db.Update at all.
	Context("with the pre-transaction expiry filter composed outside", func() {
		var filteredBatch libkafka.MessageHandlerBatch

		BeforeEach(func() {
			filteredBatch = libkafka.NewMessageHandlerBatchFilter(
				command.NewExpiredCommandSkip(
					expiryWindow,
					libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
						return libtime.DateTime(time.Now())
					}),
				),
				messageBatch,
			)
		})

		It("opens no transaction when every command in the batch has expired", func() {
			messages := make([]*sarama.ConsumerMessage, 100)
			for i := range messages {
				messages[i] = expiredMessage()
			}

			err := filteredBatch.ConsumeMessages(ctx, messages)
			Expect(err).To(BeNil())
			Expect(db.UpdateCallCount()).To(Equal(0))
		})

		It("still opens one transaction for a message the filter keeps", func() {
			err := filteredBatch.ConsumeMessages(
				ctx,
				[]*sarama.ConsumerMessage{{Value: []byte("not-json")}},
			)
			Expect(err).To(BeNil())
			Expect(db.UpdateCallCount()).To(Equal(1))
		})

		It("forwards only the live messages of a mixed batch", func() {
			inner := &kafkamocks.KafkaMessageHandlerBatch{}
			live := &sarama.ConsumerMessage{Value: []byte("not-json")}
			mixed := libkafka.NewMessageHandlerBatchFilter(
				command.NewExpiredCommandSkip(
					expiryWindow,
					libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
						return libtime.DateTime(time.Now())
					}),
				),
				inner,
			)

			err := mixed.ConsumeMessages(
				ctx,
				[]*sarama.ConsumerMessage{expiredMessage(), live, expiredMessage()},
			)
			Expect(err).To(BeNil())
			Expect(inner.ConsumeMessagesCallCount()).To(Equal(1))
			_, forwarded := inner.ConsumeMessagesArgsForCall(0)
			Expect(forwarded).To(Equal([]*sarama.ConsumerMessage{live}))
		})
	})
})
