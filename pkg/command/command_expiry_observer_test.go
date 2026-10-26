// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdtime "time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

// observedDrop records one observation, so a spec can assert both what the observer was told and
// that it was told exactly once.
type observedDrop struct {
	command base.Command
	message *sarama.ConsumerMessage
}

// recordingObserver is a hand-written ExpiredCommandObserver: the interface has one method, so a
// counterfeiter fake would be more machinery than the two fields it replaces.
type recordingObserver struct {
	drops []observedDrop
}

func (o *recordingObserver) ObserveExpired(
	_ context.Context,
	cmd base.Command,
	msg *sarama.ConsumerMessage,
) {
	o.drops = append(o.drops, observedDrop{command: cmd, message: msg})
}

// recordingHandler records every batch it is handed, so a spec can assert the wrapper forwards
// once and in order rather than per message.
type recordingHandler struct {
	calls [][]*sarama.ConsumerMessage
	err   error
}

func (h *recordingHandler) ConsumeMessages(
	_ context.Context,
	messages []*sarama.ConsumerMessage,
) error {
	h.calls = append(h.calls, messages)
	return h.err
}

var _ = Describe("NewExpiredCommandBatchHandler", func() {
	const expiryWindow = 60 * stdtime.Minute

	var now stdtime.Time
	var observer *recordingObserver
	var handler *recordingHandler
	var batchHandler func(context.Context, []*sarama.ConsumerMessage) error

	// message builds a consumer message carrying a command whose RequestTime makes the expiry
	// predicate's verdict explicit at the call site. The offset identifies the message in the
	// assertions.
	message := func(offset int64, requestTime stdtime.Time) *sarama.ConsumerMessage {
		value, err := json.Marshal(base.Command{
			RequestID:   base.RequestID(fmt.Sprintf("request-%d", offset)),
			Operation:   "create-task",
			RequestTime: requestTime,
		})
		Expect(err).To(BeNil())
		return &sarama.ConsumerMessage{
			Topic:     "master-agent-task-v1-request",
			Partition: 0,
			Offset:    offset,
			Value:     value,
		}
	}

	BeforeEach(func() {
		now = stdtime.Date(2026, 10, 24, 12, 0, 0, 0, stdtime.UTC)
		observer = &recordingObserver{}
		handler = &recordingHandler{}
		inner := command.NewExpiredCommandBatchHandler(
			command.NewExpiredCommandSkip(
				expiryWindow,
				libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
					return libtime.DateTime(now)
				}),
			),
			observer,
			handler,
		)
		batchHandler = inner.ConsumeMessages
	})

	It("forwards the kept messages to the handler in one call, in order", func() {
		first := message(1, now.Add(-5*stdtime.Minute))
		dropped := message(2, now.Add(-61*stdtime.Minute))
		third := message(3, now.Add(-30*stdtime.Minute))

		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{first, dropped, third})).
			To(BeNil())

		Expect(handler.calls).To(HaveLen(1))
		Expect(handler.calls[0]).To(Equal([]*sarama.ConsumerMessage{first, third}))
	})

	It("observes the dropped message with the decoded command and its coordinates", func() {
		dropped := message(7, now.Add(-61*stdtime.Minute))

		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{dropped})).To(BeNil())

		Expect(observer.drops).To(HaveLen(1))
		Expect(observer.drops[0].message).To(BeIdenticalTo(dropped))
		Expect(observer.drops[0].command.Operation).To(Equal(base.CommandOperation("create-task")))
		Expect(observer.drops[0].command.RequestTime).To(BeTemporally("==", now.Add(-61*stdtime.Minute)))
	})

	It("observes each dropped message exactly once", func() {
		dropped1 := message(1, now.Add(-90*stdtime.Minute))
		kept := message(2, now.Add(-1*stdtime.Minute))
		dropped3 := message(3, now.Add(-120*stdtime.Minute))

		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{dropped1, kept, dropped3})).
			To(BeNil())

		Expect(observer.drops).To(HaveLen(2))
		Expect(observer.drops[0].message).To(BeIdenticalTo(dropped1))
		Expect(observer.drops[1].message).To(BeIdenticalTo(dropped3))
	})

	It("observes nothing when no message is dropped", func() {
		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{
			message(1, now.Add(-1*stdtime.Minute)),
			message(2, now.Add(-2*stdtime.Minute)),
		})).To(BeNil())

		Expect(observer.drops).To(BeEmpty())
		Expect(handler.calls).To(HaveLen(1))
	})

	It("does not call the handler when every message is dropped", func() {
		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{
			message(1, now.Add(-61*stdtime.Minute)),
			message(2, now.Add(-62*stdtime.Minute)),
		})).To(BeNil())

		Expect(handler.calls).To(BeEmpty())
		Expect(observer.drops).To(HaveLen(2))
	})

	It("does not call the handler for an empty batch", func() {
		Expect(batchHandler(context.Background(), nil)).To(BeNil())
		Expect(handler.calls).To(BeEmpty())
		Expect(observer.drops).To(BeEmpty())
	})

	It("returns the handler error unchanged", func() {
		handler.err = errors.New("handler failed")

		Expect(batchHandler(context.Background(), []*sarama.ConsumerMessage{
			message(1, now.Add(-1*stdtime.Minute)),
		})).To(MatchError("handler failed"))
	})

	It("returns the context error instead of forwarding a cancelled batch", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(batchHandler(ctx, []*sarama.ConsumerMessage{
			message(1, now.Add(-1*stdtime.Minute)),
		})).To(MatchError(context.Canceled))

		Expect(handler.calls).To(BeEmpty())
		Expect(observer.drops).To(BeEmpty())
	})
})

var _ = Describe("NewLoggingExpiredCommandObserver", func() {
	var fakeMetrics *mocks.Metrics
	var counter prometheus.Counter
	var observer command.ExpiredCommandObserver

	BeforeEach(func() {
		fakeMetrics = &mocks.Metrics{}
		counter = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_commands_expired_total",
			Help: "test counter",
		})
		fakeMetrics.CommandExpiredTotalReturns(counter)
		observer = command.NewLoggingExpiredCommandObserver(fakeMetrics)
	})

	It("increments the counter for the dropped command's operation", func() {
		observer.ObserveExpired(
			context.Background(),
			base.Command{RequestID: "request-1", Operation: "create-task"},
			&sarama.ConsumerMessage{},
		)

		Expect(fakeMetrics.CommandExpiredTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.CommandExpiredTotalArgsForCall(0)).To(Equal("create-task"))
		Expect(testutil.ToFloat64(counter)).To(Equal(1.0))
	})

	It("counts each drop separately", func() {
		msg := &sarama.ConsumerMessage{}
		observer.ObserveExpired(context.Background(), base.Command{Operation: "update"}, msg)
		observer.ObserveExpired(context.Background(), base.Command{Operation: "update"}, msg)

		Expect(fakeMetrics.CommandExpiredTotalCallCount()).To(Equal(2))
		Expect(testutil.ToFloat64(counter)).To(Equal(2.0))
	})
})
