// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
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

// decrementRecurringTaskTitle splits title into its slug prefix and period-token
// suffix on the final " - " separator, decrements the token to the prior period
// per docs/period-token-semantics.md, and returns "<slug> - <prior-token>".
// It returns an error if the title has no " - <token>" suffix or the token suffix
// matches none of the six recognized shapes (caller must no-op on error).
func decrementRecurringTaskTitle(ctx context.Context, title string) (string, error) {
	idx := strings.LastIndex(title, " - ")
	if idx < 0 {
		return "", errors.Errorf(ctx, "title %q has no period-token suffix", title)
	}
	slug := title[:idx]
	token := title[idx+len(" - "):]
	priorToken, err := decrementPeriodToken(ctx, token)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "decrement period token %q", token)
	}
	return slug + " - " + priorToken, nil
}

// decrementPeriodToken returns the prior-period token for a single period-token
// string, recognizing the kind by shape (order per docs/period-token-semantics.md).
func decrementPeriodToken(ctx context.Context, token string) (string, error) {
	// Daily: YYYY-MM-DD
	if m := dailyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		prior := t.AddDate(0, 0, -1)
		return prior.Format("2006-01-02"), nil
	}

	// Weekday (list): YYYYWww-<3-letter-abbrev>
	if m := weekdayRe.FindStringSubmatch(token); m != nil {
		isoYear, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		abbrev := m[3]
		priorISOYear, priorWeek := decrementISOWeek(isoYear, week)
		return fmt.Sprintf("%04dW%02d-%s", priorISOYear, priorWeek, abbrev), nil
	}

	// Weekly: YYYYWww
	if m := weeklyRe.FindStringSubmatch(token); m != nil {
		isoYear, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		priorISOYear, priorWeek := decrementISOWeek(isoYear, week)
		return fmt.Sprintf("%04dW%02d", priorISOYear, priorWeek), nil
	}

	// Monthly: YYYY-MM
	if m := monthlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		t := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		prior := t.AddDate(0, -1, 0)
		return prior.Format("2006-01"), nil
	}

	// Quarterly: YYYYQq
	if m := quarterlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		if q > 1 {
			return fmt.Sprintf("%04dQ%d", year, q-1), nil
		}
		return fmt.Sprintf("%04dQ4", year-1), nil
	}

	// Yearly: YYYY
	if m := yearlyRe.FindStringSubmatch(token); m != nil {
		year, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("%04d", year-1), nil
	}

	return "", errors.Errorf(ctx, "period token %q matches no recognized shape", token)
}

// decrementISOWeek returns the ISO year and week number for the week immediately
// preceding the given ISO year and week.
func decrementISOWeek(isoYear, week int) (int, int) {
	// Find the Monday of the given ISO week.
	// Jan 4 is always in ISO week 1.
	jan4 := time.Date(isoYear, time.January, 4, 0, 0, 0, 0, time.UTC)
	// Weekday of Jan 4 (Monday = 1, ... Sunday = 7).
	jan4Weekday := int(jan4.Weekday())
	if jan4Weekday == 0 {
		jan4Weekday = 7
	}
	// Monday of ISO week 1.
	mondayOfWeek1 := jan4.AddDate(0, 0, -(jan4Weekday - 1))
	// Monday of the target week.
	mondayOfTargetWeek := mondayOfWeek1.AddDate(0, 0, (week-1)*7)
	// Go back one week.
	priorMonday := mondayOfTargetWeek.AddDate(0, 0, -7)
	// Get the ISO week of that Monday.
	priorISOYear, priorWeek := priorMonday.ISOWeek()
	return priorISOYear, priorWeek
}
