// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package publisher_test

import (
	"context"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libkafka "github.com/bborbe/kafka"
	kafkamocks "github.com/bborbe/kafka/mocks"
	"github.com/bborbe/log"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/publisher"
)

// This golden test proves the exact Kafka topic name published by
// pkg/publisher.TaskPublisher (via cdb.NewEventObjectSender, wired the same
// way as main.go), both with an explicit TopicPrefix and with an empty one
// (unprefixed topic, no leading dash).
var _ = Describe("Published event topic (TopicPrefix golden test)", func() {
	var (
		ctx             context.Context
		fakeProducer    *kafkamocks.KafkaSyncProducer
		schemaID        cdb.SchemaID
		currentDateTime libtime.CurrentDateTime
		tp              publisher.TaskPublisher
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeProducer = &kafkamocks.KafkaSyncProducer{}
		fakeProducer.SendMessageReturns(0, 0, nil)
		schemaID = lib.TaskV1SchemaID
		currentDateTime = libtime.NewCurrentDateTime()
	})

	buildPublisher := func(prefix base.TopicPrefix) publisher.TaskPublisher {
		eventObjectSender := cdb.NewEventObjectSender(
			libkafka.NewJSONSender(fakeProducer, log.DefaultSamplerFactory),
			prefix,
			log.DefaultSamplerFactory,
		)
		return publisher.NewTaskPublisher(eventObjectSender, schemaID, currentDateTime)
	}

	Context("with a non-empty TopicPrefix", func() {
		It("publishes to the prefixed topic", func() {
			tp = buildPublisher(base.TopicPrefix("develop"))

			task := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("test-uuid-1234"),
				Frontmatter: lib.TaskFrontmatter{
					"status": "next",
				},
				Content: lib.TaskContent("# Test"),
			}
			Expect(tp.PublishChanged(ctx, task)).To(Succeed())

			Expect(fakeProducer.SendMessageCallCount()).To(Equal(1))
			_, msg := fakeProducer.SendMessageArgsForCall(0)
			Expect(msg.Topic).To(Equal("develop-agent-task-v1-event"))
		})
	})

	Context("with the master (prod) TopicPrefix", func() {
		It("publishes to the prefixed topic", func() {
			tp = buildPublisher(base.TopicPrefix("master"))

			task := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("test-uuid-9012"),
				Frontmatter: lib.TaskFrontmatter{
					"status": "next",
				},
				Content: lib.TaskContent("# Test"),
			}
			Expect(tp.PublishChanged(ctx, task)).To(Succeed())

			Expect(fakeProducer.SendMessageCallCount()).To(Equal(1))
			_, msg := fakeProducer.SendMessageArgsForCall(0)
			Expect(msg.Topic).To(Equal("master-agent-task-v1-event"))
		})
	})

	Context("with an empty TopicPrefix", func() {
		It("publishes to the unprefixed topic (no leading dash)", func() {
			tp = buildPublisher(base.TopicPrefix(""))

			task := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("test-uuid-5678"),
				Frontmatter: lib.TaskFrontmatter{
					"status": "next",
				},
				Content: lib.TaskContent("# Test"),
			}
			Expect(tp.PublishChanged(ctx, task)).To(Succeed())

			Expect(fakeProducer.SendMessageCallCount()).To(Equal(1))
			_, msg := fakeProducer.SendMessageArgsForCall(0)
			Expect(msg.Topic).To(Equal("agent-task-v1-event"))
		})
	})
})
