// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// Regexps compiled at package scope (not inside functions).
var (
	dailyRe     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	weekdayRe   = regexp.MustCompile(`^(\d{4})W(\d{2})-([a-z]{3})$`)
	weeklyRe    = regexp.MustCompile(`^(\d{4})W(\d{2})$`)
	monthlyRe   = regexp.MustCompile(`^(\d{4})-(\d{2})$`)
	quarterlyRe = regexp.MustCompile(`^(\d{4})Q([1-4])$`)
	yearlyRe    = regexp.MustCompile(`^(\d{4})$`)
)

// isoWeekMonday returns the Monday time.Time of the given ISO year and week.
// Jan 4 is always in ISO week 1.
func isoWeekMonday(isoYear, week int) time.Time {
	jan4 := time.Date(isoYear, time.January, 4, 0, 0, 0, 0, time.UTC)
	jan4Weekday := int(jan4.Weekday())
	if jan4Weekday == 0 {
		jan4Weekday = 7
	}
	mondayOfWeek1 := jan4.AddDate(0, 0, -(jan4Weekday - 1))
	mondayOfTargetWeek := mondayOfWeek1.AddDate(0, 0, (week-1)*7)
	return mondayOfTargetWeek
}

// parsePeriodTokenOrdinal parses a single period-token string into a
// monotonically-increasing ordinal: a more-recent period yields a strictly
// larger ordinal than any earlier period of the same recurrence kind.
// Recognition order matches docs/period-token-semantics.md (Daily, Weekday,
// Weekly, Monthly, Quarterly, Yearly). Returns an error if the token matches
// no recognized shape (caller must skip such candidates).
func parsePeriodTokenOrdinal(ctx context.Context, token string) (int64, error) {
	// Daily: YYYY-MM-DD
	if m := dailyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		return t.UTC().Unix(), nil
	}

	// Weekday (list): YYYYWww-<3-letter-abbrev>
	if m := weekdayRe.FindStringSubmatch(token); m != nil {
		isoYear, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		// Abbrev is ignored for ordering — all weekdays of the same week rank
		// together by their week's Monday; ties are broken by the caller's
		// stable sort on descending Title.
		return isoWeekMonday(isoYear, week).UTC().Unix(), nil
	}

	// Weekly: YYYYWww
	if m := weeklyRe.FindStringSubmatch(token); m != nil {
		isoYear, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		return isoWeekMonday(isoYear, week).UTC().Unix(), nil
	}

	// Monthly: YYYY-MM
	if m := monthlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Unix(), nil
	}

	// Quarterly: YYYYQq
	if m := quarterlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		return time.Date(year, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC).Unix(), nil
	}

	// Yearly: YYYY
	if m := yearlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(), nil
	}

	return 0, errors.Errorf(ctx, "period token %q matches no recognized shape", token)
}

// rankedCandidate pairs a candidate title with its parsed period-token ordinal.
type rankedCandidate struct {
	Title   string
	Ordinal int64
}

// splitTitleToken splits a recurring-task title into its slug prefix and
// period-token suffix on the FINAL " - " separator. Returns ok=false when the
// title has no " - <token>" suffix. The slug may itself contain " - ".
func splitTitleToken(title string) (slug string, token string, ok bool) {
	idx := strings.LastIndex(title, " - ")
	if idx < 0 {
		return "", "", false
	}
	return title[:idx], title[idx+len(" - "):], true
}

// rankSameSlugCandidatesDescending parses each candidate title's period-token
// ordinal and returns the candidates whose token is a recognized shape, sorted
// most-recent-first (largest ordinal first). Candidates whose token matches no
// shape are dropped (logged at V(3)). The sort is stable; equal ordinals keep a
// deterministic order by descending Title so redelivery is idempotent.
func rankSameSlugCandidatesDescending(ctx context.Context, titles []string) []rankedCandidate {
	var ranked []rankedCandidate
	for _, title := range titles {
		slug, token, ok := splitTitleToken(title)
		if !ok {
			glog.V(3).
				Infof("auto-supersede: candidate %q has no period-token suffix, skipping", title)
			continue
		}
		ordinal, err := parsePeriodTokenOrdinal(ctx, token)
		if err != nil {
			glog.V(3).
				Infof("auto-supersede: candidate %q token unrecognized, skipping: %v", title, err)
			continue
		}
		ranked = append(ranked, rankedCandidate{Title: title, Ordinal: ordinal})
		_ = slug // slug retained for future scan use; ordinal is sufficient for ranking
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		return a.Ordinal > b.Ordinal || (a.Ordinal == b.Ordinal && a.Title > b.Title)
	})
	return ranked
}
