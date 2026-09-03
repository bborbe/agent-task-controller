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

var _ = Describe("MergeFrontmatter", func() {
	It(
		"reports zero guard decisions when the incoming counter equals the on-disk counter",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
			_, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(0))
		},
	)

	It(
		"reports zero decisions when a JSON-decoded float64 counter equals the YAML-decoded int counter",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
			incoming := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": float64(3),
			}
			_, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(0))
		},
	)

	It(
		"reports exactly one decision naming the field and both values when the incoming counter differs",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1}
			_, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("trigger_count"))
			Expect(decisions[0].Kept).To(Equal(3))
			Expect(decisions[0].Rejected).To(Equal(1))
		},
	)

	It(
		"reports a decision when the on-disk counter is an int64 and the incoming differs (numeric comparison)",
		func() {
			existing := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": int64(3),
			}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1}
			_, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("trigger_count"))
		},
	)

	It(
		"keeps a non-integer on-disk counter verbatim and still reports a decision (DB failure-mode)",
		func() {
			existing := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": "3",
			}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("trigger_count"))
			Expect(merged["trigger_count"]).To(Equal("3"))
		},
	)

	It(
		"does not panic when both counters hold the same uncomparable type (map)",
		func() {
			existing := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": map[string]interface{}{"bad": 1},
			}
			incoming := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": map[string]interface{}{"other": 2},
			}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(merged["trigger_count"]).To(Equal(map[string]interface{}{"bad": 1}))
		},
	)

	It(
		"does not panic when both counters hold the same uncomparable type (slice)",
		func() {
			existing := lib.TaskFrontmatter{
				"status":      "in_progress",
				"retry_count": []interface{}{1, 2},
			}
			incoming := lib.TaskFrontmatter{
				"status":      "in_progress",
				"retry_count": []interface{}{3},
			}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(merged["retry_count"]).To(Equal([]interface{}{1, 2}))
		},
	)

	It(
		"reports exactly one decision naming assignee with the kept and rejected values when the on-disk assignee differs",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "other"}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("assignee"))
			Expect(decisions[0].Kept).To(Equal("claude"))
			Expect(decisions[0].Rejected).To(Equal("other"))
			Expect(merged["assignee"]).To(Equal("claude"))
		},
	)

	It("reports zero decisions when the on-disk assignee equals the incoming assignee", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
		merged, decisions := result.MergeFrontmatter(existing, incoming)
		Expect(decisions).To(HaveLen(0))
		Expect(merged["assignee"]).To(Equal("claude"))
	})

	It(
		"applies an incoming empty assignee over a non-empty on-disk value without reporting a decision (deliverer clear exception)",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": ""}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(0))
			Expect(merged["assignee"]).To(Equal(""))
		},
	)

	It("reports zero decisions when an incoming assignee introduces a key absent on disk", func() {
		existing := lib.TaskFrontmatter{"status": "in_progress"}
		incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "backtest-agent"}
		merged, decisions := result.MergeFrontmatter(existing, incoming)
		Expect(decisions).To(HaveLen(0))
		Expect(merged["assignee"]).To(Equal("backtest-agent"))
	})

	It(
		"keeps the on-disk previous_assignee over a differing incoming value and reports one decision",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "A"}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "B"}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("previous_assignee"))
			Expect(decisions[0].Kept).To(Equal("A"))
			Expect(decisions[0].Rejected).To(Equal("B"))
			Expect(merged["previous_assignee"]).To(Equal("A"))
		},
	)

	It(
		"does not treat an empty incoming previous_assignee as a clear — the on-disk value wins and a decision is reported",
		func() {
			existing := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "A"}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": ""}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("previous_assignee"))
			Expect(merged["previous_assignee"]).To(Equal("A"))
		},
	)

	It(
		"reports both the counter decision and the operator-owned decision when both differ (additive rule)",
		func() {
			existing := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": 3,
				"assignee":      "claude",
			}
			incoming := lib.TaskFrontmatter{
				"status":        "in_progress",
				"trigger_count": 1,
				"assignee":      "other",
			}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(2))
			Expect(decisions[0].Field).To(Equal("trigger_count"))
			Expect(decisions[1].Field).To(Equal("assignee"))
			Expect(merged["trigger_count"]).To(Equal(3))
			Expect(merged["assignee"]).To(Equal("claude"))
		},
	)

	It(
		"does not panic on a non-string incoming assignee and keeps the on-disk value",
		func() {
			// A malformed payload can decode assignee as an uncomparable slice; the
			// empty-clear comparison must type-assert, never == on any (spec 007,
			// same doctrine as frontmatterValueEqual).
			existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
			incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": []any{"x"}}
			merged, decisions := result.MergeFrontmatter(existing, incoming)
			Expect(decisions).To(HaveLen(1))
			Expect(decisions[0].Field).To(Equal("assignee"))
			Expect(merged["assignee"]).To(Equal("claude"))
		},
	)
})
