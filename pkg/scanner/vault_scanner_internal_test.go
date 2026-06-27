// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

var _ = Describe("injectAndStore", func() {
	counterValue := func(reason string) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}

	It(
		"increments inject_task_identifier_failed counter when InjectTaskIdentifier returns error",
		func() {
			v := &vaultScanner{
				metrics: metrics.New(),
				ops: fileOps{
					readFile: func(_ context.Context, _ string) ([]byte, error) {
						return nil, nil
					},
					writeFile: func(_ context.Context, _ string, _ []byte) error {
						return nil
					},
				},
				autoInject: true,
			}

			initial := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
			initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
			initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
			initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
			initialReadFailed := counterValue(metrics.ReasonReadFailed)

			// Content without frontmatter delimiter causes InjectTaskIdentifier to fail
			task, written, werr := v.injectAndStore(
				context.Background(),
				[]byte("no frontmatter at all"),
				"rel.md",
				"",
			)
			Expect(task).To(BeNil())
			Expect(written).To(Equal(""))
			Expect(werr).To(BeFalse())
			Expect(counterValue(metrics.ReasonInjectTaskIdentifierFailed)).To(Equal(initial + 1))

			// Other reason labels must not tick (compared to initial values)
			Expect(
				counterValue(metrics.ReasonInvalidFrontmatter),
			).To(BeNumerically("==", initialInvalid))
			Expect(
				counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
			).To(BeNumerically("==", initialDupInvalid))
			Expect(
				counterValue(metrics.ReasonEmptyStatus),
			).To(BeNumerically("==", initialEmptyStatus))
			Expect(
				counterValue(metrics.ReasonReadFailed),
			).To(BeNumerically("==", initialReadFailed))
		},
	)
})
