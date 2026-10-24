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
	kvmocks "github.com/bborbe/kv/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/factory"
)

// These pin the wiring newCommandConsumerFunc installs. Reverting the offset manager to the
// store manager, or dropping the expiry filter, would otherwise pass every other test in this
// repo because each component is tested on its own.
var _ = Describe("command consumer composition", func() {
	var ctx context.Context
	var currentDateTime libtime.CurrentDateTimeGetter

	BeforeEach(func() {
		ctx = context.Background()
		currentDateTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(time.Now())
		})
	})

	It("starts from the TTL-seek manager, not the store manager's oldest sentinel", func() {
		offsetManager := factory.NewCommandOffsetManager(
			&kafkamocks.KafkaSaramaClient{},
			&kvmocks.DB{},
			currentDateTime,
		)
		Expect(offsetManager.InitialOffset()).To(Equal(libkafka.OffsetNewest))
		Expect(offsetManager.FallbackOffset()).To(Equal(libkafka.OffsetNewest))
	})

	It("drops an expired command before the wrapped handler runs", func() {
		inner := &kafkamocks.KafkaMessageHandlerBatch{}
		handler := factory.NewCommandHandler(inner, currentDateTime)
		value, err := json.Marshal(base.Command{
			RequestID:   "1234567890",
			RequestTime: time.Now().Add(-2 * time.Hour),
			Initiator:   "me",
			Operation:   "any-op",
		})
		Expect(err).To(BeNil())

		Expect(
			handler.ConsumeMessages(ctx, []*sarama.ConsumerMessage{{Value: value}}),
		).To(Succeed())
		Expect(inner.ConsumeMessagesCallCount()).To(Equal(0))
	})
})
