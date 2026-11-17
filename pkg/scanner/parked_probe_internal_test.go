// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("isParkedProbe", func() {
	// record builds the parsed frontmatter the metrics pass hands the predicate. Only
	// these four keys matter: the clause set is task_type + phase + status +
	// current_job.
	record := func(taskType, phase, status, currentJob string) map[string]interface{} {
		return map[string]interface{}{
			"task_type":   taskType,
			"phase":       phase,
			"status":      status,
			"current_job": currentJob,
		}
	}

	It("counts a probe parked at hold with no job running", func() {
		Expect(isParkedProbe(record("healthcheck", "human_review", "hold", ""))).To(BeTrue())
	})

	It("counts a probe parked at a non-terminal phase with status in_progress", func() {
		// The population's parked rows read both `hold` and `in_progress`, so a clause
		// matching only one of them would go green while missing the other.
		Expect(isParkedProbe(record("healthcheck", "execution", "in_progress", ""))).To(BeTrue())
	})

	It("does not count a probe whose job is running", func() {
		Expect(isParkedProbe(record(
			"healthcheck", "planning", "in_progress", "claude-headless-771a0318",
		))).To(BeFalse())
	})

	It("does not count a probe at a terminal phase", func() {
		Expect(isParkedProbe(record("healthcheck", "done", "in_progress", ""))).To(BeFalse())
	})

	It("does not count a probe with a terminal status and a non-terminal phase", func() {
		for _, status := range []string{"completed", "aborted"} {
			Expect(isParkedProbe(record("healthcheck", "planning", status, ""))).
				To(BeFalse(), "status %q must not count", status)
		}
	})

	It("does not count a record whose task_type is not healthcheck", func() {
		Expect(isParkedProbe(record("llm", "planning", "in_progress", ""))).To(BeFalse())
		Expect(
			isParkedProbe(record("github-update-go", "planning", "in_progress", "")),
		).To(BeFalse())
	})

	It("does not count a record carrying no phase or no status", func() {
		Expect(isParkedProbe(record("healthcheck", "", "hold", ""))).To(BeFalse())
		Expect(isParkedProbe(record("healthcheck", "planning", "", ""))).To(BeFalse())
	})

	It("does not panic on a record carrying none of the keys", func() {
		Expect(isParkedProbe(map[string]interface{}{})).To(BeFalse())
	})
})
