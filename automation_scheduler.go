package main

import (
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/automations"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// automationScheduleLookback is how far back a schedule's last two fires are
// looked for — the snapshot scheduler's bound, so a monthly schedule still
// yields a period to bound its jitter by.
const automationScheduleLookback = staleBackupLookback

// StartAutomations runs schedule-triggered automations on the scheduler that
// already runs snapshot schedules.
//
// There is ONE scheduler in the control plane, not two: the same ticker loop,
// the same deterministic per-entity jitter, the same catch-up window, and the
// same claim-by-unique-insert — automation_runs is keyed (automation_id,
// scheduled_at) exactly as database_snapshot_runs is keyed (instance,
// scheduled_at). A second scheduler would be a second implementation of all four.
func (s *databaseScheduler) StartAutomations(ex *automations.Executor) {
	go s.runLoop("automation-scheduler", schedulerTick, func() { s.dispatchDueAutomations(ex) })
	go s.runLoop("automation-run-timeout", time.Minute, ex.FailStuckRuns)
	logger.Info("automation", "automation scheduler started", logger.F{
		"tick":                schedulerTick.String(),
		"run_budget":          automations.RUN_BUDGET.String(),
		"max_concurrent_runs": automations.MAX_CONCURRENT_RUNS,
	})
}

// dispatchDueAutomations fires every enabled schedule-triggered automation whose
// slot is due. Claiming a slot is the insert of its run row, so the many ticks
// that see the same slot fire it once.
func (s *databaseScheduler) dispatchDueAutomations(ex *automations.Executor) {
	scheduled, err := query.ListScheduledAutomations(db.DB)
	if err != nil {
		logger.Error("automation", "failed to list scheduled automations", logger.F{"error": err})
		return
	}

	now := time.Now().UTC()
	for i := range scheduled {
		a := scheduled[i]

		fires := cronPreviousFires(a.Trigger.Cron, now, 2, automationScheduleLookback)
		if len(fires) == 0 {
			continue
		}
		slot := fires[0]
		period := time.Duration(0)
		if len(fires) > 1 {
			period = fires[0].Sub(fires[1])
		}

		// A slot from before the automation was last defined or enabled was never
		// owed. Without this, creating a "0 3 * * *" automation at 10:00 would
		// immediately record a skipped run for a 03:00 slot nobody asked for.
		if slot.Before(a.UpdatedAt) {
			continue
		}

		if now.Before(slot.Add(s.jitterForKey(fmt.Sprintf("automation:%d", a.ID), period))) {
			continue
		}

		// Too old to be worth running — recorded as a skipped run, not dropped.
		skip := ""
		if age := now.Sub(slot); age > schedulerCatchUpWindow {
			skip = fmt.Sprintf("slot was %s old; beyond the %s catch-up window", age.Round(time.Minute), schedulerCatchUpWindow)
		}
		ex.FireScheduled(&a, slot, skip)
	}
}
