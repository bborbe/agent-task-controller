// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"strings"

	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/result"
)

const capEscalationHeading = "## Trigger Cap Escalation"

var _ = Describe("CappedEscalatedStatus", func() {
	It("parks the record in hold, the only status the executor's trigger set excludes", func() {
		Expect(string(result.CappedEscalatedStatus)).To(Equal("hold"))
	})
})

var _ = Describe("CollapseTriggerCapEscalation", func() {
	It("keeps exactly one section when a same-second race appended three", func() {
		// Shaped after probe-sentry-fix-agent-prod on 2026-11-09: three sections, all
		// stamped 2026-11-09T14:17:36Z, each separated by unrelated body content.
		body := "intro\n\n" +
			"## Trigger Cap Escalation\n\n- **Timestamp:** 2026-11-09T14:17:36Z\n\n" +
			"## Result\n\nfirst\n\n" +
			"## Trigger Cap Escalation\n\n- **Timestamp:** 2026-11-09T14:17:36Z\n\n" +
			"## Result\n\nsecond\n\n" +
			"## Trigger Cap Escalation\n\n- **Timestamp:** 2026-11-09T14:17:36Z\n"

		collapsed := result.CollapseTriggerCapEscalation(body)

		Expect(strings.Count(collapsed, capEscalationHeading)).To(Equal(1))
		Expect(collapsed).To(ContainSubstring("- **Timestamp:** 2026-11-09T14:17:36Z"))
		// The content the duplicates sat between must survive: collapsing removes the
		// duplicate sections, never the neighbours.
		Expect(collapsed).To(ContainSubstring("## Result\n\nfirst"))
		Expect(collapsed).To(ContainSubstring("## Result\n\nsecond"))
	})

	It("returns a body with no section unchanged", func() {
		body := "intro\n\n## Result\n\nnothing escalated here\n"

		Expect(result.CollapseTriggerCapEscalation(body)).To(Equal(body))
	})

	It("is idempotent", func() {
		body := "intro\n\n" +
			capEscalationHeading + "\n\n- **Timestamp:** A\n\n" +
			capEscalationHeading + "\n\n- **Timestamp:** A\n"

		once := result.CollapseTriggerCapEscalation(body)

		Expect(result.CollapseTriggerCapEscalation(once)).To(Equal(once))
	})
})

var _ = Describe("applyTriggerCap", func() {
	newAtCap := func() lib.TaskFrontmatter {
		return lib.TaskFrontmatter{
			"assignee":      "sentry-fix-agent",
			"status":        "in_progress",
			"phase":         "planning",
			"ref":           "7e9586437f04d7d37a7be0df5fec3879bd44fe94",
			"trigger_count": 3,
		}
	}

	It("parks a cap-escalated record in hold so a re-dispatch cannot restore it", func() {
		merged := newAtCap()
		existing := newAtCap()

		body := result.ApplyTriggerCapForTest(merged, existing, 3, "## Result\n\nfailed\n")

		Expect(string(merged.Status())).To(Equal(string(result.CappedEscalatedStatus)))
		Expect(string(merged.Assignee())).To(Equal(""))
		Expect(merged["previous_assignee"]).To(Equal("sentry-fix-agent"))
		Expect(strings.Count(body, capEscalationHeading)).To(Equal(1))
	})

	It("re-parks an already-escalated record and collapses its duplicate sections", func() {
		merged := newAtCap()
		merged["assignee"] = ""
		merged["previous_assignee"] = "sentry-fix-agent"
		existing := newAtCap()
		existing["status"] = "in_progress"
		body := "## Result\n\nfailed\n\n" +
			capEscalationHeading + "\n\n- **Timestamp:** A\n\n" +
			capEscalationHeading + "\n\n- **Timestamp:** A\n"

		written := result.ApplyTriggerCapForTest(merged, existing, 3, body)

		// A record escalated by an older build is still in_progress on disk; the next
		// write must move it to hold, or it stays selectable by the executor forever.
		Expect(string(merged.Status())).To(Equal(string(result.CappedEscalatedStatus)))
		Expect(strings.Count(written, capEscalationHeading)).To(Equal(1))
	})

	It("leaves a record below the cap alone", func() {
		merged := lib.TaskFrontmatter{
			"assignee":      "sentry-fix-agent",
			"status":        "in_progress",
			"ref":           "7e9586437f04d7d37a7be0df5fec3879bd44fe94",
			"trigger_count": 1,
		}
		existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1}
		body := "## Result\n\nfine\n"

		written := result.ApplyTriggerCapForTest(merged, existing, 1, body)

		Expect(string(merged.Status())).To(Equal("in_progress"))
		Expect(string(merged.Assignee())).To(Equal("sentry-fix-agent"))
		Expect(written).To(Equal(body))
	})
})
