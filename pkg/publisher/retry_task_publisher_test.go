// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package publisher_test

import (
	"context"
	"errors"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/run"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/publisher"
)

var _ = Describe("RetryTaskPublisher", func() {
	var (
		ctx            context.Context
		inner          *mocks.TaskPublisher
		fakeMetrics    *mocks.Metrics
		retryCounter   prometheus.Counter
		failureCounter prometheus.Counter
		retryLabels    []string
		failureLabels  []string
		recordedWaits  []time.Duration
		waiter         run.Waiter
		tp             publisher.TaskPublisher
		task           lib.Task
		id             lib.TaskIdentifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		inner = &mocks.TaskPublisher{}
		task = lib.Task{TaskIdentifier: lib.TaskIdentifier("test-uuid-1234")}
		id = lib.TaskIdentifier("24 Tasks/deleted.md")

		// Dedicated, non-registered counters so repeated specs never collide in
		// the default registry (promauto would panic on a duplicate name).
		retryCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_retry_total"})
		failureCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_failure_total"})
		retryLabels = nil
		failureLabels = nil

		fakeMetrics = &mocks.Metrics{}
		fakeMetrics.KafkaPublishRetryTotalStub = func(outcome string) prometheus.Counter {
			retryLabels = append(retryLabels, outcome)
			return retryCounter
		}
		fakeMetrics.KafkaPublishFailureTotalStub = func(outcome string) prometheus.Counter {
			failureLabels = append(failureLabels, outcome)
			return failureCounter
		}

		recordedWaits = nil
		waiter = run.WaiterFunc(func(_ context.Context, wait time.Duration) error {
			recordedWaits = append(recordedWaits, wait)
			return nil
		})

		tp = publisher.NewRetryTaskPublisher(inner, fakeMetrics, waiter)
	})

	It("publishes once without retrying on first-attempt success", func() {
		inner.PublishChangedReturns(nil)

		Expect(tp.PublishChanged(ctx, task)).To(Succeed())

		Expect(inner.PublishChangedCallCount()).To(Equal(1))
		Expect(testutil.ToFloat64(retryCounter)).To(Equal(0.0))
		Expect(testutil.ToFloat64(failureCounter)).To(Equal(0.0))
		Expect(recordedWaits).To(BeEmpty())
	})

	It("retries then succeeds, incrementing the retry counter per retry", func() {
		publishErr := errors.New("transient kafka error")
		inner.PublishChangedReturnsOnCall(0, publishErr)
		inner.PublishChangedReturnsOnCall(1, publishErr)
		inner.PublishChangedReturns(nil)

		Expect(tp.PublishChanged(ctx, task)).To(Succeed())

		Expect(inner.PublishChangedCallCount()).To(Equal(3))
		Expect(testutil.ToFloat64(retryCounter)).To(Equal(2.0))
		Expect(testutil.ToFloat64(failureCounter)).To(Equal(0.0))
		Expect(recordedWaits).To(Equal([]time.Duration{1 * time.Second, 2 * time.Second}))
		Expect(retryLabels).To(Equal([]string{"changed", "changed"}))
	})

	It("gives up after the full backoff schedule and returns the last error", func() {
		publishErr := errors.New("persistent kafka error")
		inner.PublishChangedReturns(publishErr)

		err := tp.PublishChanged(ctx, task)

		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, publishErr)).To(BeTrue())
		Expect(inner.PublishChangedCallCount()).To(Equal(5))
		Expect(testutil.ToFloat64(retryCounter)).To(Equal(4.0))
		Expect(testutil.ToFloat64(failureCounter)).To(Equal(1.0))
		Expect(recordedWaits).To(Equal([]time.Duration{
			1 * time.Second,
			2 * time.Second,
			4 * time.Second,
			8 * time.Second,
		}))
		Expect(failureLabels).To(Equal([]string{"changed"}))
	})

	It("stops immediately when the context is cancelled during a wait", func() {
		inner.PublishChangedReturns(errors.New("transient kafka error"))
		waiter = run.WaiterFunc(func(_ context.Context, wait time.Duration) error {
			recordedWaits = append(recordedWaits, wait)
			return context.Canceled
		})
		tp = publisher.NewRetryTaskPublisher(inner, fakeMetrics, waiter)

		err := tp.PublishChanged(ctx, task)

		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(inner.PublishChangedCallCount()).To(Equal(1))
		Expect(testutil.ToFloat64(retryCounter)).To(Equal(0.0))
		Expect(testutil.ToFloat64(failureCounter)).To(Equal(0.0))
		Expect(recordedWaits).To(Equal([]time.Duration{1 * time.Second}))
	})

	It("retries PublishDeleted identically and labels the metrics deleted", func() {
		publishErr := errors.New("transient kafka error")
		inner.PublishDeletedReturnsOnCall(0, publishErr)
		inner.PublishDeletedReturnsOnCall(1, publishErr)
		inner.PublishDeletedReturns(nil)

		Expect(tp.PublishDeleted(ctx, id)).To(Succeed())

		Expect(inner.PublishDeletedCallCount()).To(Equal(3))
		Expect(testutil.ToFloat64(retryCounter)).To(Equal(2.0))
		Expect(testutil.ToFloat64(failureCounter)).To(Equal(0.0))
		Expect(recordedWaits).To(Equal([]time.Duration{1 * time.Second, 2 * time.Second}))
		Expect(retryLabels).To(Equal([]string{"deleted", "deleted"}))
	})
})
