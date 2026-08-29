// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner_test

import (
	"context"

	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/scanner"
)

var _ = Describe("ParseTask", func() {
	var ctx = context.Background()

	It("parses a task with valid frontmatter", func() {
		content := []byte(
			"---\ntask_identifier: 11111111-1111-1111-1111-111111111111\nstatus: in_progress\n---\nbody\n",
		)
		task := scanner.ParseTask(ctx, content)
		Expect(task).NotTo(BeNil())
		Expect(
			task.TaskIdentifier,
		).To(Equal(lib.TaskIdentifier("11111111-1111-1111-1111-111111111111")))
		Expect(string(task.Frontmatter.Status())).To(Equal("in_progress"))
		Expect(string(task.Content)).To(Equal("body\n"))
	})

	It("returns nil for content without frontmatter", func() {
		Expect(scanner.ParseTask(ctx, []byte("no frontmatter\n"))).To(BeNil())
	})

	It("returns nil for content without a task_identifier", func() {
		content := []byte("---\nstatus: in_progress\n---\nbody\n")
		Expect(scanner.ParseTask(ctx, content)).To(BeNil())
	})

	It("returns nil for invalid yaml frontmatter", func() {
		content := []byte("---\ntask_identifier: [unclosed\n---\nbody\n")
		Expect(scanner.ParseTask(ctx, content)).To(BeNil())
	})

	It("keeps the last value when frontmatter has duplicate keys", func() {
		content := []byte(
			"---\ntask_identifier: 11111111-1111-1111-1111-111111111111\nstatus: in_progress\nstatus: completed\n---\nbody\n",
		)
		task := scanner.ParseTask(ctx, content)
		Expect(task).NotTo(BeNil())
		Expect(string(task.Frontmatter.Status())).To(Equal("completed"))
	})
})
