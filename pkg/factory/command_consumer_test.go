// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	libkafka "github.com/bborbe/kafka"
	kafkamocks "github.com/bborbe/kafka/mocks"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/factory"
)

// These pin the wiring newCommandConsumerFunc installs. Reverting the offset manager to the
// store manager, or dropping the expiry filter, would otherwise pass every other test in this
// repo because each component is tested on its own.
var _ = Describe("command consumer composition", func() {
	// must mirror pkg/factory commandExpireDuration
	const expiryWindow = 60 * time.Minute

	var ctx context.Context
	var topic libkafka.Topic
	var partition libkafka.Partition
	var now time.Time
	var currentDateTime libtime.CurrentDateTimeGetter

	BeforeEach(func() {
		ctx = context.Background()
		topic = libkafka.Topic("my-topic")
		partition = libkafka.Partition(0)
		now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
		currentDateTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	})

	It("starts from the TTL-seek manager, not the store manager's oldest sentinel", func() {
		offsetManager := factory.NewCommandOffsetManager(
			&kafkamocks.KafkaSaramaClient{},
			&kafkamocks.KafkaOffsetStore{},
			currentDateTime,
		)
		Expect(offsetManager.InitialOffset()).To(Equal(libkafka.OffsetNewest))
		Expect(offsetManager.FallbackOffset()).To(Equal(libkafka.OffsetNewest))
	})

	It("seeks to now minus the expiry window on a store miss and persists the result", func() {
		saramaClient := &kafkamocks.KafkaSaramaClient{}
		saramaClient.GetOffsetReturns(5000, nil)
		offsetStore := &kafkamocks.KafkaOffsetStore{}
		offsetStore.GetReturns(0, libkv.KeyNotFoundError)

		offsetManager := factory.NewCommandOffsetManager(
			saramaClient,
			offsetStore,
			currentDateTime,
		)
		offset, err := offsetManager.NextOffset(ctx, topic, partition)
		Expect(err).To(BeNil())
		Expect(offset).To(Equal(libkafka.Offset(5000)))

		seekTopic, seekPartition, seekTimestamp := saramaClient.GetOffsetArgsForCall(0)
		Expect(seekTopic).To(Equal(topic.String()))
		Expect(seekPartition).To(Equal(partition.Int32()))
		Expect(seekTimestamp).To(Equal(now.Add(-expiryWindow).UnixMilli()))

		_, _, _, storedOffset := offsetStore.SetArgsForCall(0)
		Expect(storedOffset).To(Equal(libkafka.Offset(5000)))
	})

	It("drops an expired command before the wrapped handler runs", func() {
		inner := &kafkamocks.KafkaMessageHandlerBatch{}
		counter := prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_commands_expired_total",
			Help: "test counter",
		})
		fakeMetrics := &mocks.Metrics{}
		fakeMetrics.CommandExpiredTotalReturns(counter)
		handler := factory.NewCommandHandler(
			inner,
			currentDateTime,
			command.NewLoggingExpiredCommandObserver(fakeMetrics),
		)
		value, err := json.Marshal(base.Command{
			RequestID:   "1234567890",
			RequestTime: now.Add(-2 * time.Hour),
			Initiator:   "me",
			Operation:   "any-op",
		})
		Expect(err).To(BeNil())

		Expect(
			handler.ConsumeMessages(ctx, []*sarama.ConsumerMessage{{Value: value}}),
		).To(Succeed())
		Expect(inner.ConsumeMessagesCallCount()).To(Equal(0))

		// The drop must also be recorded: a drop that only stops the handler is the
		// invisible failure this composition exists to end.
		Expect(fakeMetrics.CommandExpiredTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.CommandExpiredTotalArgsForCall(0)).To(Equal("any-op"))
		Expect(testutil.ToFloat64(counter)).To(Equal(1.0))
	})
})
