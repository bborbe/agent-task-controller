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

var _ = Describe("ClearAssigneeIfHumanReview", func() {
	DescribeTable(
		"clears an agent assignee at human_review and retains a configured human one",
		func(phase string, assignee string, humans result.HumanAssignees, wantCleared bool) {
			merged := lib.TaskFrontmatter{"phase": phase, "assignee": assignee}

			previous := result.ClearAssigneeIfHumanReview(merged, humans)

			if wantCleared {
				Expect(merged["assignee"]).To(Equal(""))
				Expect(previous).To(Equal(assignee))
				// clearAssignee captures the prior owner only when there was one, so an
				// already-unowned record gains no previous_assignee key.
				if assignee == "" {
					Expect(merged).NotTo(HaveKey("previous_assignee"))
					return
				}
				Expect(merged["previous_assignee"]).To(Equal(assignee))
				return
			}
			Expect(merged["assignee"]).To(Equal(assignee))
			Expect(merged).NotTo(HaveKey("previous_assignee"))
			Expect(previous).To(Equal(""))
		},
		Entry(
			"an agent assignee at human_review is cleared (spec-039/042 purpose preserved)",
			"human_review", "pr-reviewer-agent", result.ParseHumanAssignees("bborbe"), true,
		),
		Entry(
			"a configured human assignee at human_review is retained",
			"human_review", "bborbe", result.ParseHumanAssignees("bborbe"), false,
		),
		Entry(
			"a human assignee is retained among several configured humans",
			"human_review", "alice", result.ParseHumanAssignees("bborbe, alice"), false,
		),
		Entry(
			"an assignee at a non-human_review phase is untouched",
			"execution", "pr-reviewer-agent", result.ParseHumanAssignees("bborbe"), false,
		),
		Entry(
			"an unconfigured set clears every assignee (the pre-existing default)",
			"human_review", "bborbe", result.ParseHumanAssignees(""), true,
		),
		Entry(
			"an empty assignee at human_review stays empty and records no previous_assignee",
			"human_review", "", result.ParseHumanAssignees("bborbe"), true,
		),
	)

	It("retains a human owner even when the set is populated by a differently-spaced list", func() {
		merged := lib.TaskFrontmatter{"phase": "human_review", "assignee": "bborbe"}
		result.ClearAssigneeIfHumanReview(merged, result.ParseHumanAssignees(" bborbe ,  "))
		Expect(merged["assignee"]).To(Equal("bborbe"))
	})
})

var _ = Describe("ParseHumanAssignees", func() {
	It("ignores empty entries and surrounding whitespace", func() {
		humans := result.ParseHumanAssignees(" bborbe , , alice ")
		Expect(humans.Contains("bborbe")).To(BeTrue())
		Expect(humans.Contains("alice")).To(BeTrue())
		Expect(humans.Contains("")).To(BeFalse())
		Expect(humans.IsEmpty()).To(BeFalse())
	})

	It("yields an empty set for an unset value", func() {
		Expect(result.ParseHumanAssignees("").IsEmpty()).To(BeTrue())
	})
})
