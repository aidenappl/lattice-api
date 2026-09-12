package main

import (
	"time"

	"github.com/aidenappl/lattice-api/cron"
)

// The control plane's 5-field cron evaluator, matching the one the runner
// schedules with, lives in package cron so the automations package can VALIDATE
// schedule triggers with the same parser these schedulers evaluate with — a
// second parser that could disagree about what an expression means is exactly
// the thing to avoid. These wrappers keep the snapshot scheduler's and the
// staleness alarm's call sites unchanged.
//
// The control plane needs cron to answer one question the runner cannot: has a
// scheduled backup been *missed*? A schedule that never fires emits nothing, so
// absence is only detectable by someone who knows when the run was due. That is
// the failure GitLab's postmortem turned on — backups had been broken for a long
// time and the only signal, cron email, was being silently discarded.

// cronPreviousFires returns up to count fire times at or before `from`, newest
// first, looking back at most maxLookback.
func cronPreviousFires(expr string, from time.Time, count int, maxLookback time.Duration) []time.Time {
	return cron.PreviousFires(expr, from, count, maxLookback)
}

// cronMatches reports whether a 5-field expression fires at t.
// Fields: minute hour day-of-month month day-of-week.
func cronMatches(expr string, t time.Time) bool {
	return cron.Matches(expr, t)
}
