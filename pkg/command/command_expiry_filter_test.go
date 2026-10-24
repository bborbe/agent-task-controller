// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"encoding/json"
	stdtime "time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("NewExpiredCommandSkip", func() {
	const expiryWindow = 60 * stdtime.Minute

	var now stdtime.Time
	var skip func(*sarama.ConsumerMessage) bool

	BeforeEach(func() {
		now = stdtime.Date(2026, 10, 24, 12, 0, 0, 0, stdtime.UTC)
		skip = command.NewExpiredCommandSkip(
			expiryWindow,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
		)
	})

	message := func(command base.Command) *sarama.ConsumerMessage {
		value, err := json.Marshal(command)
		Expect(err).To(BeNil())
		return &sarama.ConsumerMessage{Value: value}
	}

	It("keeps a command younger than the window", func() {
		msg := message(base.Command{RequestTime: now.Add(-30 * stdtime.Minute)})
		Expect(skip(msg)).To(BeFalse())
	})

	It("drops a command older than the window", func() {
		msg := message(base.Command{RequestTime: now.Add(-61 * stdtime.Minute)})
		Expect(skip(msg)).To(BeTrue())
	})

	It("keeps a command exactly at the window boundary", func() {
		msg := message(base.Command{RequestTime: now.Add(-expiryWindow)})
		Expect(skip(msg)).To(BeFalse())
	})

	It("keeps a message that is not valid JSON", func() {
		Expect(skip(&sarama.ConsumerMessage{Value: []byte("not json")})).To(BeFalse())
	})

	It("drops a payload that parses but carries no RequestTime", func() {
		Expect(skip(&sarama.ConsumerMessage{Value: []byte("{}")})).To(BeTrue())
	})

	It("keeps a command with a future RequestTime", func() {
		msg := message(base.Command{RequestTime: now.Add(5 * stdtime.Minute)})
		Expect(skip(msg)).To(BeFalse())
	})
})
