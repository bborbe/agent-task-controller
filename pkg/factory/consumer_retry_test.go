// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/factory"
)

var _ = Describe("RetryConsumer", func() {
	const delay = time.Millisecond

	It("returns nil and stops after the first clean return", func() {
		calls := 0
		consumer := func(_ context.Context) error {
			calls++
			return nil
		}
		err := factory.RetryConsumer(consumer, delay)(context.Background())
		Expect(err).To(BeNil())
		Expect(calls).To(Equal(1))
	})

	It("re-enters the consumer after a transient error instead of propagating it", func() {
		calls := 0
		consumer := func(_ context.Context) error {
			calls++
			if calls < 3 {
				return errors.Errorf(
					context.Background(),
					"kafka: error while consuming topic/0: EOF",
				)
			}
			return nil
		}
		err := factory.RetryConsumer(consumer, delay)(context.Background())
		Expect(err).To(BeNil())
		Expect(calls).To(Equal(3))
	})

	It("returns nil rather than an error when the context ends during the wait", func() {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		consumer := func(_ context.Context) error {
			calls++
			cancel()
			return errors.Errorf(context.Background(), "kafka: error while consuming topic/0: EOF")
		}
		err := factory.RetryConsumer(consumer, time.Hour)(ctx)
		Expect(err).To(BeNil())
		Expect(calls).To(Equal(1))
	})
})
