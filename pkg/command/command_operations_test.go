// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	"github.com/bborbe/cqrs/base"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent/task/controller/pkg/command"
)

var _ = Describe("controller CommandOperation validation", func() {
	var ctx context.Context
	BeforeEach(func() { ctx = context.Background() })

	DescribeTable(
		"all controller-local CommandOperation constants pass base.CommandOperation.Validate",
		func(op base.CommandOperation) {
			Expect(op.Validate(ctx)).To(Succeed())
		},
		Entry("TaskResultCommandOperation", command.TaskResultCommandOperation),
		Entry("IncrementFrontmatterCommandOperation", command.IncrementFrontmatterCommandOperation),
		Entry("UpdateFrontmatterCommandOperation", command.UpdateFrontmatterCommandOperation),
	)
})
