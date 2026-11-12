// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	task "github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

var _ = Describe("buildIncrementPatch", func() {
	newIncrement := func() task.IncrementFrontmatterCommand {
		return task.IncrementFrontmatterCommand{Field: "trigger_count", Delta: 1}
	}

	It("parks the record in hold in the same write that reaches the cap", func() {
		frontmatter := map[string]any{
			"assignee":      "sentry-fix-agent",
			"status":        "in_progress",
			"phase":         "planning",
			"ref":           "7e9586437f04d7d37a7be0df5fec3879bd44fe94",
			"trigger_count": 2,
		}

		patch, escalation := command.BuildIncrementPatch(
			newIncrement(),
			frontmatter,
			"private-agent",
		)

		Expect(escalation).NotTo(BeNil())
		Expect(patch["trigger_count"]).To(Equal(3))
		Expect(patch["assignee"]).To(Equal(""))
		Expect(patch["previous_assignee"]).To(Equal("sentry-fix-agent"))
		// The whole point: without this the executor restores the assignee on its next
		// reconcile tick and the scanner re-arms the budget, so the row returns to the
		// queue as a fresh candidate. `hold` is refused by the executor's own filter.
		Expect(patch["status"]).To(Equal(string(result.CappedEscalatedStatus)))
	})

	It("does not park a record below the cap", func() {
		frontmatter := map[string]any{
			"assignee":      "sentry-fix-agent",
			"status":        "in_progress",
			"ref":           "7e9586437f04d7d37a7be0df5fec3879bd44fe94",
			"trigger_count": 0,
		}

		patch, escalation := command.BuildIncrementPatch(
			newIncrement(),
			frontmatter,
			"private-agent",
		)

		Expect(escalation).To(BeNil())
		Expect(patch).NotTo(HaveKey("status"))
		Expect(patch).NotTo(HaveKey("assignee"))
	})
})
