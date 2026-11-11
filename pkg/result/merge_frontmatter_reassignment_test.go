// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/result"
)

// The 2026-11-10 loss on OCTOPUS-PROD-3Q: the sentry fix handoff published
// assignee: sentry-fix-agent together with assignee_reassigned_by, and the
// merge kept the on-disk analyzer while leaving the declaration behind.
// v0.11.3's applyOperatorOwnedFields has no reassignment exception, so this
// specification fails on that build and passes on the fixed one.
var _ = Describe("MergeFrontmatter reassignment declaration", func() {
	It("applies a declared reassignment and consumes the declaration", func() {
		existing := lib.TaskFrontmatter{
			"assignee":  "sentry-analyzer-agent",
			"task_type": "sentry-deep-analyzer",
			"phase":     "execution",
			"status":    "in_progress",
		}
		incoming := lib.TaskFrontmatter{
			"assignee":               "sentry-fix-agent",
			"assignee_reassigned_by": "sentry-deep-execution-fix-handoff",
			"task_type":              "sentry-fix",
			"phase":                  "planning",
			"status":                 "in_progress",
		}

		merged, _ := result.MergeFrontmatter(existing, incoming)

		Expect(merged["assignee"]).To(Equal("sentry-fix-agent"))
		Expect(merged).NotTo(HaveKey("assignee_reassigned_by"))
		Expect(merged["task_type"]).To(Equal("sentry-fix"))
		Expect(merged["phase"]).To(Equal("planning"))
	})

	It("keeps the on-disk assignee when the payload carries no declaration", func() {
		existing := lib.TaskFrontmatter{
			"assignee":  "sentry-analyzer-agent",
			"task_type": "sentry-deep-analyzer",
		}
		incoming := lib.TaskFrontmatter{
			"assignee":  "sentry-fix-agent",
			"task_type": "sentry-fix",
		}

		merged, _ := result.MergeFrontmatter(existing, incoming)

		Expect(merged["assignee"]).To(Equal("sentry-analyzer-agent"))
	})
})
