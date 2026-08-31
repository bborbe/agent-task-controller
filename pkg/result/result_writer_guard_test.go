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
})
