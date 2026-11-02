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
	"github.com/bborbe/cqrs/mocks"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	kvmocks "github.com/bborbe/kv/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("command consumer expiry window", func() {
	const expiryWindow = 60 * time.Minute // must mirror pkg/factory commandExpireDuration

	var (
		ctx                   context.Context
		commandMessageHandler libkafka.MessageHandlerTx
		executor              *mocks.CDBCommandObjectExecutorTx
	)

	BeforeEach(func() {
		ctx = context.Background()
		executor = &mocks.CDBCommandObjectExecutorTx{}
		executor.CommandOperationReturns(base.CommandOperation("any-op"))
		executor.HandleCommandReturns(nil, base.Event{}, nil)
		commandMessageHandler = cdb.NewCommandObjectMessageHandlerTx(
			lib.TaskV1SchemaID,
			cdb.NewCommandObjectHandlerTx(true, executor),
			expiryWindow,
		)
	})

	command := func(age time.Duration) base.Command {
		return base.Command{
			RequestID:   "1234567890",
			RequestTime: time.Now().Add(-age),
			Initiator:   "me",
			Operation:   "any-op",
		}
	}

	consume := func(command base.Command) error {
		value, err := json.Marshal(command)
		Expect(err).To(BeNil())
		return commandMessageHandler.ConsumeMessage(
			ctx,
			&kvmocks.Tx{},
			&sarama.ConsumerMessage{Value: value},
		)
	}

	It("executes a command 30 minutes old", func() {
		err := consume(command(30 * time.Minute))
		Expect(err).To(BeNil())
		Expect(executor.HandleCommandCallCount()).To(Equal(1))
	})

	It("drops a command 61 minutes old as expired without executing it", func() {
		err := consume(command(61 * time.Minute))
		Expect(err).NotTo(BeNil())
		Expect(errors.Cause(err)).To(Equal(cdb.CommandExpiredError))
		Expect(executor.HandleCommandCallCount()).To(Equal(0))
	})
})
