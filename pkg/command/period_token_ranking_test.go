// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("parsePeriodTokenOrdinal", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("ordinal ordering within each kind", func() {
		DescribeTable(
			"ordinal(older) < ordinal(newer)",
			func(older string, newer string) {
				olderOrd, err := command.ParsePeriodTokenOrdinal(ctx, older)
				Expect(err).To(BeNil())
				newerOrd, err := command.ParsePeriodTokenOrdinal(ctx, newer)
				Expect(err).To(BeNil())
				Expect(olderOrd).To(BeNumerically("<", newerOrd))
			},
			// Weekly boundary
			Entry("Weekly ISO-week year-boundary", "2025W52", "2026W01"),
			Entry("Weekly steady", "2026W26", "2026W27"),
			// Weekday boundary
			Entry("Weekday ISO-week year-boundary", "2025W52-mon", "2026W01-mon"),
			// Quarterly boundary
			Entry("Quarterly boundary", "2025Q4", "2026Q1"),
			// Monthly boundary
			Entry("Monthly boundary", "2025-12", "2026-01"),
			// Daily boundary
			Entry("Daily boundary", "2025-12-31", "2026-01-01"),
			// Yearly
			Entry("Yearly", "2025", "2026"),
		)
	})

	Describe("weekday of same week ties", func() {
		It("mon and fri of the same week have equal ordinal", func() {
			ordMon, err := command.ParsePeriodTokenOrdinal(ctx, "2026W28-mon")
			Expect(err).To(BeNil())
			ordFri, err := command.ParsePeriodTokenOrdinal(ctx, "2026W28-fri")
			Expect(err).To(BeNil())
			Expect(ordMon).To(Equal(ordFri))
		})
	})

	Describe("unrecognized token", func() {
		It("returns error and ordinal 0", func() {
			ord, err := command.ParsePeriodTokenOrdinal(ctx, "notaperiod")
			Expect(err).To(HaveOccurred())
			Expect(ord).To(Equal(int64(0)))
		})
	})
})

var _ = Describe("splitTitleToken", func() {
	DescribeTable(
		"cases",
		func(title string, expSlug string, expToken string, expOk bool) {
			slug, token, ok := command.SplitTitleToken(title)
			Expect(slug).To(Equal(expSlug))
			Expect(token).To(Equal(expToken))
			Expect(ok).To(Equal(expOk))
		},
		Entry("simple slug",
			"Aquascape PWC - 2026W27",
			"Aquascape PWC", "2026W27", true),
		Entry("slug containing separator",
			"Foo - Bar - 2026W27",
			"Foo - Bar", "2026W27", true),
		Entry("no separator",
			"NoSeparator",
			"", "", false),
	)
})

var _ = Describe("rankSameSlugCandidatesDescending", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("orders most-recent-first and drops unrecognized tokens", func() {
		// Deliberately shuffled; 2025W52 should sort last (oldest across year boundary).
		titles := []string{
			"IBKR Swing Trading - 2025W52",
			"IBKR Swing Trading - 2026W02",
			"IBKR Swing Trading - 2026W01",
			"IBKR Swing Trading - notatoken",
		}
		result := command.RankSameSlugCandidatesDescending(ctx, titles)
		// notatoken is dropped.
		Expect(result).To(HaveLen(3))
		// Most-recent-first: 2026W02 > 2026W01 > 2025W52.
		Expect(result[0].Title).To(Equal("IBKR Swing Trading - 2026W02"))
		Expect(result[1].Title).To(Equal("IBKR Swing Trading - 2026W01"))
		Expect(result[2].Title).To(Equal("IBKR Swing Trading - 2025W52"))
	})

	It("weekday same-week tie is broken by descending Title (stable sort)", func() {
		// All three are 2026W28 (same ordinal). Tie-break: descending Title.
		// "Sched - 2026W28-wed" > "Sched - 2026W28-mon" > "Sched - 2026W28-fri"
		// (string comparison: 'w' > 'm' > 'f').
		titles := []string{
			"Sched - 2026W28-mon",
			"Sched - 2026W28-wed",
			"Sched - 2026W28-fri",
		}
		result := command.RankSameSlugCandidatesDescending(ctx, titles)
		Expect(result).To(HaveLen(3))
		Expect(result[0].Title).To(Equal("Sched - 2026W28-wed"))
		Expect(result[1].Title).To(Equal("Sched - 2026W28-mon"))
		Expect(result[2].Title).To(Equal("Sched - 2026W28-fri"))
	})
})
