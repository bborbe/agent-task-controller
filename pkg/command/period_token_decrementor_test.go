// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("decrementRecurringTaskTitle", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	DescribeTable(
		"success cases",
		func(title string, expected string) {
			result, err := command.DecrementRecurringTaskTitle(ctx, title)
			Expect(err).To(BeNil())
			Expect(result).To(Equal(expected))
		},
		// Daily
		Entry("Daily mid-year", "Cleanup Inbox - 2026-06-15", "Cleanup Inbox - 2026-06-14"),
		Entry("Daily year-boundary", "Cleanup Inbox - 2026-01-01", "Cleanup Inbox - 2025-12-31"),
		// Weekday (list)
		Entry("Weekday mid-year", "Standup - 2026W27-sat", "Standup - 2026W26-sat"),
		Entry("Weekday year-boundary", "Standup - 2026W01-mon", "Standup - 2025W52-mon"),
		// Weekly
		Entry("Weekly mid-year", "Aquascape PWC - 2026W27", "Aquascape PWC - 2026W26"),
		Entry(
			"Weekly ISO-week year-boundary",
			"Aquascape PWC - 2026W01",
			"Aquascape PWC - 2025W52",
		),
		// Monthly
		Entry("Monthly mid-year", "Pay Rent - 2026-06", "Pay Rent - 2026-05"),
		Entry("Monthly year-boundary", "Pay Rent - 2026-01", "Pay Rent - 2025-12"),
		// Quarterly
		Entry("Quarterly mid-year", "Quarterly Review - 2026Q2", "Quarterly Review - 2026Q1"),
		Entry("Quarterly year-boundary", "Quarterly Review - 2026Q1", "Quarterly Review - 2025Q4"),
		// Yearly
		Entry("Yearly", "Annual Filing - 2026", "Annual Filing - 2025"),
		Entry("Yearly decade boundary", "Annual Filing - 2020", "Annual Filing - 2019"),
		// Slug containing the separator
		Entry("Slug containing separator", "Foo - Bar - 2026W27", "Foo - Bar - 2026W26"),
	)

	DescribeTable("error cases",
		func(title string) {
			_, err := command.DecrementRecurringTaskTitle(ctx, title)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, nil)).To(BeFalse())
		},
		Entry("No suffix separator", "RandomTask"),
		Entry("Unrecognized token shape", "Weird Task - notaperiod"),
		Entry("Empty token after separator", "Bad - "),
	)
})
