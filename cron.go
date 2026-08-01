package main

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// A 5-field cron evaluator, matching the one the runner schedules with.
//
// The control plane needs this to answer one question the runner cannot: has a
// scheduled backup been *missed*? A schedule that never fires emits nothing, so
// absence is only detectable by someone who knows when the run was due. That is
// the failure GitLab's postmortem turned on — backups had been broken for a long
// time and the only signal, cron email, was being silently discarded.
//
// Deliberately a matcher walked backwards rather than a parser producing a "next
// run" instant: it is the same logic as the runner's, so the two cannot disagree
// about what a given expression means.

// cronPreviousFires returns up to count fire times at or before `from`, newest
// first, looking back at most maxLookback.
func cronPreviousFires(expr string, from time.Time, count int, maxLookback time.Duration) []time.Time {
	if len(strings.Fields(expr)) != 5 || count <= 0 {
		return nil
	}

	var fires []time.Time
	t := from.UTC().Truncate(time.Minute)
	limit := from.Add(-maxLookback)

	for t.After(limit) {
		if cronMatches(expr, t) {
			fires = append(fires, t)
			if len(fires) == count {
				return fires
			}
		}
		t = t.Add(-time.Minute)
	}
	return fires
}

// cronMatches reports whether a 5-field expression fires at t.
// Fields: minute hour day-of-month month day-of-week.
func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}

	checks := []struct {
		field string
		value int
		min   int
		max   int
	}{
		{fields[0], t.Minute(), 0, 59},
		{fields[1], t.Hour(), 0, 23},
		{fields[2], t.Day(), 1, 31},
		{fields[3], int(t.Month()), 1, 12},
		{fields[4], int(t.Weekday()), 0, 6},
	}

	for _, c := range checks {
		matched, err := cronFieldMatches(c.field, c.value, c.min, c.max)
		if err != nil || !matched {
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, value, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		matched, err := cronTermMatches(strings.TrimSpace(part), value, min, max)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func cronTermMatches(term string, value, min, max int) (bool, error) {
	var step int
	rangeExpr := term
	if idx := strings.Index(term, "/"); idx != -1 {
		s, err := strconv.Atoi(term[idx+1:])
		if err != nil || s <= 0 {
			return false, errInvalidCron
		}
		step = s
		rangeExpr = term[:idx]
	}

	var rangeMin, rangeMax int
	switch {
	case rangeExpr == "*":
		rangeMin, rangeMax = min, max
	case strings.Contains(rangeExpr, "-"):
		idx := strings.Index(rangeExpr, "-")
		lo, err := strconv.Atoi(rangeExpr[:idx])
		if err != nil {
			return false, errInvalidCron
		}
		hi, err := strconv.Atoi(rangeExpr[idx+1:])
		if err != nil || lo > hi {
			return false, errInvalidCron
		}
		rangeMin, rangeMax = lo, hi
	default:
		n, err := strconv.Atoi(rangeExpr)
		if err != nil {
			return false, errInvalidCron
		}
		if step == 0 {
			return value == n, nil
		}
		rangeMin, rangeMax = n, max
	}

	if step == 0 {
		return value >= rangeMin && value <= rangeMax, nil
	}
	if value < rangeMin || value > rangeMax {
		return false, nil
	}
	return (value-rangeMin)%step == 0, nil
}

// errInvalidCron marks an unparseable field. The specific parse error is never
// actionable — an expression either matches or it does not.
var errInvalidCron = errors.New("invalid cron field")
