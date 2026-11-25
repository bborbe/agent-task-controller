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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/factory"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// This pins the property the 2026-11-24 losses needed and the library's handler
// chain does not have: a command whose vault write fails transiently must NOT be
// committed past. The batch handler returning an error is what stops
// kafka_consumer-offset.go:498-505 from calling MarkOffset, so the consumer
// re-reads the same offset on the next pass.
var _ = Describe("command handler batch", func() {
	const expiryWindow = 60 * time.Minute

	var ctx context.Context
	var db *kvmocks.DB
	var producer *kafkamocks.KafkaSyncProducer
	var messages []*sarama.ConsumerMessage

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
		Expect(err).To(BeNil())
		messages = []*sarama.ConsumerMessage{
			{Topic: "commands", Partition: 0, Offset: 780982, Value: value},
		}
	})

	newHandler := func(executor cdb.CommandObjectExecutorTx) libkafka.MessageHandlerBatch {
		return factory.CreateCommandHandlerBatch(
			db,
			producer,
			lib.TaskV1SchemaID,
			true,
			base.TopicPrefix("myprefix"),
			expiryWindow,
			executor,
		)
	}

	It("reports failure on a transient error and success on the retry", func() {
		attempts := 0
		handler := newHandler(
			cdb.CommandObjectExecutorTxFunc(
				"create-task",
				true,
				func(
					_ context.Context,
					_ libkv.Tx,
					_ cdb.CommandObject,
				) (*base.EventID, base.Event, error) {
					attempts++
					if attempts == 1 {
						return nil, nil, errors.Wrapf(
							ctx,
							vaultapi.ErrTransient,
							"create task: unexpected status 503",
						)
					}
					return nil, nil, nil
				},
			),
		)

		// First pass: the vault is down. The error must escape so the offset is
		// not committed, and no result may be published (the command is not
		// decided yet).
		err := handler.ConsumeMessages(ctx, messages)
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeTrue())
		Expect(producer.SendMessageCallCount()).To(Equal(0))

		// Second pass, same message, vault recovered: the command lands and
		// publishes exactly one Success result.
		Expect(handler.ConsumeMessages(ctx, messages)).To(Succeed())
		Expect(attempts).To(Equal(2))
		Expect(producer.SendMessageCallCount()).To(Equal(1))
	})

	It("reports success and publishes a Failure result for a permanent error", func() {
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

		// A decided failure must not block the partition: the offset advances and
		// the Failure result is the record of it.
		Expect(handler.ConsumeMessages(ctx, messages)).To(Succeed())
		Expect(producer.SendMessageCallCount()).To(Equal(1))
	})
})
