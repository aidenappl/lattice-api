// Package cron is the control plane's one 5-field cron evaluator, matching the
// one the runner schedules with.
//
// It lives in its own package so that more than package main can use it. The
// snapshot scheduler and the backup-staleness alarm evaluate expressions; the
// automations package has to VALIDATE them at save time. A second parser for
// that — one that could disagree with the evaluator about what an expression
// means — is exactly what this package exists to prevent.
//
// Deliberately a matcher walked backwards rather than a parser producing a "next
// run" instant: it is the same logic as the runner's, so the two cannot disagree
// about what a given expression means.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid marks an unparseable field. The specific parse error is never
// actionable when matching — an expression either matches or it does not.
var ErrInvalid = errors.New("invalid cron field")

type fieldSpec struct {
	name string
	min  int
	max  int
}

// fields is minute hour day-of-month month day-of-week, in expression order.
var fields = [5]fieldSpec{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day-of-month", 1, 31},
	{"month", 1, 12},
	{"day-of-week", 0, 6},
}

// PreviousFires returns up to count fire times at or before `from`, newest
// first, looking back at most maxLookback.
func PreviousFires(expr string, from time.Time, count int, maxLookback time.Duration) []time.Time {
	if len(strings.Fields(expr)) != 5 || count <= 0 {
		return nil
	}

	var fires []time.Time
	t := from.UTC().Truncate(time.Minute)
	limit := from.Add(-maxLookback)

	for t.After(limit) {
		if Matches(expr, t) {
			fires = append(fires, t)
			if len(fires) == count {
				return fires
			}
		}
		t = t.Add(-time.Minute)
	}
	return fires
}

// Matches reports whether a 5-field expression fires at t.
// Fields: minute hour day-of-month month day-of-week.
func Matches(expr string, t time.Time) bool {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return false
	}

	values := [5]int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	for i, spec := range fields {
		matched, err := fieldMatches(parts[i], values[i], spec.min, spec.max)
		if err != nil || !matched {
			return false
		}
	}
	return true
}

// Validate reports why an expression is unusable, or nil if it is usable.
//
// Matching alone cannot answer this. An out-of-range value — minute 60, or
// day-of-week 7 (Sunday is 0 here) — parses, never matches, and so produces a
// schedule that saves, renders, and silently never fires. Validation goes
// through the same term parser as matching and adds the one check matching
// cannot make: every value sits inside its field's range.
func Validate(expr string) error {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return fmt.Errorf("cron expression must have 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	for i, spec := range fields {
		for _, term := range strings.Split(parts[i], ",") {
			lo, hi, _, err := parseTerm(strings.TrimSpace(term), spec.min, spec.max)
			if err != nil {
				return fmt.Errorf("%s field %q is not valid", spec.name, parts[i])
			}
			if lo < spec.min || hi > spec.max {
				return fmt.Errorf("%s field %q is outside %d-%d", spec.name, parts[i], spec.min, spec.max)
			}
		}
	}
	return nil
}

func fieldMatches(field string, value, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		matched, err := termMatches(strings.TrimSpace(part), value, min, max)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func termMatches(term string, value, min, max int) (bool, error) {
	lo, hi, step, err := parseTerm(term, min, max)
	if err != nil {
		return false, err
	}
	if value < lo || value > hi {
		return false, nil
	}
	if step == 0 {
		return true, nil
	}
	return (value-lo)%step == 0, nil
}

// parseTerm is the single parser behind both Matches and Validate. It turns one
// comma-separated term into an inclusive range and a step (0 = every value in
// the range). A bare number with no step is a one-value range; a bare number
// WITH a step ("5/15") runs from that number to the field's maximum.
func parseTerm(term string, min, max int) (lo, hi, step int, err error) {
	rangeExpr := term
	if idx := strings.Index(term, "/"); idx != -1 {
		s, convErr := strconv.Atoi(term[idx+1:])
		if convErr != nil || s <= 0 {
			return 0, 0, 0, ErrInvalid
		}
		step = s
		rangeExpr = term[:idx]
	}

	switch {
	case rangeExpr == "*":
		return min, max, step, nil
	case strings.Contains(rangeExpr, "-"):
		idx := strings.Index(rangeExpr, "-")
		a, convErr := strconv.Atoi(rangeExpr[:idx])
		if convErr != nil {
			return 0, 0, 0, ErrInvalid
		}
		b, convErr := strconv.Atoi(rangeExpr[idx+1:])
		if convErr != nil || a > b {
			return 0, 0, 0, ErrInvalid
		}
		return a, b, step, nil
	default:
		n, convErr := strconv.Atoi(rangeExpr)
		if convErr != nil {
			return 0, 0, 0, ErrInvalid
		}
		if step == 0 {
			return n, n, 0, nil
		}
		return n, max, step, nil
	}
}
