// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/result"
)

var _ = Describe("SetWalkConcurrency", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// The walk's concurrency is a process-wide knob, so every spec restores the
	// default it found. A spec that left it at a swept value would silently
	// change the read fan-out of every later spec in this suite.
	AfterEach(func() {
		Expect(result.SetWalkConcurrency(ctx, 8)).To(Succeed())
	})

	DescribeTable("refuses a value that cannot fan out",
		func(n int) {
			err := result.SetWalkConcurrency(ctx, n)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("walk concurrency must be >= 1"))
		},
		Entry("zero", 0),
		Entry("negative", -1),
	)

	It("takes effect for a value of one or more", func() {
		Expect(result.SetWalkConcurrency(ctx, 32)).To(Succeed())
		Expect(result.WalkConcurrency()).To(Equal(32))
	})

	It("leaves the effective value unchanged when it refuses", func() {
		Expect(result.SetWalkConcurrency(ctx, 16)).To(Succeed())
		Expect(result.SetWalkConcurrency(ctx, 0)).NotTo(Succeed())
		// A refused call must not move the knob: the next walk still fans out at
		// the last accepted setting, not at the rejected one.
		Expect(result.WalkConcurrency()).To(Equal(16))
	})
})
