// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("escalateCappedContent", func() {
	var ctx context.Context
	var now libtime.DateTime

	BeforeEach(func() {
		ctx = context.Background()
		now = libtime.DateTime(time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC))
	})

	// taskFile renders a task with the given frontmatter lines and a body that
	// already carries a ## Failure block, so the appended section has something to
	// sit after.
	taskFile := func(fmLines ...string) []byte {
		doc := "---\n"
		for _, line := range fmLines {
			doc += line + "\n"
		}
		doc += "---\n\nBody prose.\n\n## Failure\n\n- **Reason:**\n\n```\nno GitHub PR URL\n```\n"
		return []byte(doc)
	}

	frontmatterOf := func(content []byte) map[string]interface{} {
		fm, err := extractFrontmatter(ctx, content)
		Expect(err).NotTo(HaveOccurred())
		var out map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &out)).To(Succeed())
		return out
	}

	It("escalates a capped idle record and records why", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)

		next, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		fm := frontmatterOf(next)
		Expect(fm["assignee"]).To(Equal(""))
		Expect(fm["previous_assignee"]).To(Equal("pr-reviewer-agent"))
		Expect(fm["trigger_count"]).To(BeEquivalentTo(3))
		// The rescue must survive the executor's restore branch, which only runs for
		// a task whose status is in the config's trigger set (default: in_progress).
		Expect(fm["status"]).To(Equal("hold"))
		Expect(string(next)).To(ContainSubstring("## Trigger Cap Escalation"))
		Expect(string(next)).To(ContainSubstring("**Trigger count:** 3"))
		Expect(string(next)).To(ContainSubstring("**Assignee:** pr-reviewer-agent"))
		// The existing failure block survives; the escalation is appended after it.
		Expect(string(next)).To(ContainSubstring("## Failure"))
	})

	It("leaves a record below its cap alone", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 2",
		)

		_, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("honours an explicit max_triggers instead of the default", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: execution",
			"max_triggers: 1",
			"trigger_count: 1",
		)

		next, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(string(next)).To(ContainSubstring("**Max triggers:** 1"))
	})

	It("is idempotent once the assignee is already cleared", func() {
		content := taskFile(
			"assignee: \"\"",
			"previous_assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)

		_, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("does not strand a task whose job is still running", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"current_job: pr-reviewer-agent-abc-20261028200000",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)

		_, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("does not touch a record that already reached a terminal status", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: completed",
			"phase: done",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)

		_, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("does not engage when neither max_triggers nor ref is present", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"trigger_count: 3",
		)

		_, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("does not append a second escalation section", func() {
		content := taskFile(
			"assignee: pr-reviewer-agent",
			"status: in_progress",
			"phase: planning",
			"ref: \"0000000000000000000000000000000000000000\"",
			"trigger_count: 3",
		)
		first, changed, err := escalateCappedContent(ctx, content, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		// Re-arm as an operator re-delegating the record would: assignee and status
		// both go back, since the first escalation parked it at hold.
		fm := frontmatterOf(first)
		fm["assignee"] = "pr-reviewer-agent"
		fm["status"] = "in_progress"
		rearmed, err := yaml.Marshal(fm)
		Expect(err).NotTo(HaveOccurred())
		second := []byte("---\n" + string(rearmed) + "---\n" + extractBody(first))

		next, changed, err := escalateCappedContent(ctx, second, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(countOccurrences(string(next), "## Trigger Cap Escalation")).To(Equal(1))
	})
})

func countOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
		}
	}
	return count
}
