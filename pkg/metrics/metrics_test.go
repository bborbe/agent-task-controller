// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	_ "github.com/bborbe/agent-task-controller/pkg/metrics"
)

var _ = Describe("Metrics", func() {
	It("registers all expected metric names in the default registry", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		names := make(map[string]bool, len(mfs))
		for _, mf := range mfs {
			names[mf.GetName()] = true
		}

		Expect(names).To(HaveKey("agent_controller_scan_cycles_total"))
		Expect(names).To(HaveKey("agent_controller_tasks_published_total"))
		Expect(names).To(HaveKey("agent_controller_results_written_total"))
		Expect(names).To(HaveKey("agent_controller_git_push_total"))
		Expect(names).To(HaveKey("agent_controller_conflict_resolutions_total"))
		Expect(names).To(HaveKey("agent_controller_frontmatter_commands_total"))
		Expect(names).To(HaveKey("agent_controller_git_rest_calls_total"))
		Expect(names).To(HaveKey("agent_controller_vault_scanner_skipped_files_total"))
	})

	It("pre-initializes all scan_cycles_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_scan_cycles_total", "result")
		Expect(labels).To(ContainElements("changes", "no_changes", "error"))
	})

	It("pre-initializes all tasks_published_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_tasks_published_total", "type")
		Expect(labels).To(ContainElements("changed", "deleted"))
	})

	It("pre-initializes all results_written_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_results_written_total", "result")
		Expect(labels).To(ContainElements("success", "not_found", "error"))
	})

	It("pre-initializes all git_push_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_git_push_total", "result")
		Expect(labels).To(ContainElements("success", "retry_success", "conflict_resolved", "error"))
	})

	It("pre-initializes all conflict_resolutions_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_conflict_resolutions_total", "result")
		Expect(labels).To(ContainElements("success", "error"))
	})

	It("pre-initializes all frontmatter_commands_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_frontmatter_commands_total", "operation")
		Expect(labels).To(ContainElements("increment-frontmatter", "update-frontmatter"))

		labels = gatherLabels(mfs, "agent_controller_frontmatter_commands_total", "outcome")
		Expect(labels).To(ContainElements("success", "error", "not_found"))
	})

	It("pre-initializes all git_rest_calls_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_git_rest_calls_total", "op")
		Expect(labels).To(ContainElements("get", "post", "delete", "list", "readiness"))

		labels = gatherLabels(mfs, "agent_controller_git_rest_calls_total", "status")
		Expect(labels).To(ContainElements("success", "error"))
	})

	It("pre-initializes all vault_scanner_skipped_files_total label combinations", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		labels := gatherLabels(mfs, "agent_controller_vault_scanner_skipped_files_total", "reason")
		Expect(labels).To(ContainElements(
			"invalid_frontmatter",
			"duplicate_frontmatter_invalid",
			"empty_status",
			"inject_task_identifier_failed",
			"read_failed",
			"auto_inject_disabled",
			"repair_not_converging",
		))
	})

	It("pre-initializes repair_not_converging at zero before any skip (spec 009 AC1)", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		value, found := gatherCounterValue(
			mfs,
			"agent_controller_vault_scanner_skipped_files_total",
			"reason",
			"repair_not_converging",
		)
		Expect(found).To(BeTrue(), "repair_not_converging series must exist after init()")
		Expect(value).To(BeNumerically("==", 0))
	})
})

func gatherLabels(mfs []*dto.MetricFamily, metricName string, labelName string) []string {
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		var values []string
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == labelName {
					values = append(values, lp.GetValue())
				}
			}
		}
		return values
	}
	return nil
}

// gatherCounterValue returns the counter value of the sample of metricName whose
// label labelName equals labelValue, and whether such a sample exists at all.
func gatherCounterValue(
	mfs []*dto.MetricFamily,
	metricName string,
	labelName string,
	labelValue string,
) (float64, bool) {
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == labelName && lp.GetValue() == labelValue {
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}
