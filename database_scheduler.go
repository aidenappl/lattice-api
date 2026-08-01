package main

import (
	"fmt"
	"hash/fnv"
	"time"

	// Embed the timezone database. In a scratch/alpine image
	// time.LoadLocation errors, and a naive fallback to UTC looks like it worked
	// while being an hour wrong for half the year, for some tenants only.
	_ "time/tzdata"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

const (
	// schedulerTick is how often due slots are looked for. Finer than a minute
	// buys nothing: cron's resolution is a minute.
	schedulerTick = 30 * time.Second

	// schedulerCatchUpWindow bounds how far back a missed slot is still worth
	// running. Beyond it the slot is recorded as skipped/stale rather than run.
	//
	// Unbounded catch-up is a real failure mode: Kubernetes CronJobs wedge
	// permanently after 100 missed schedules, and a control plane that was down
	// overnight should take *one* backup on recovery, not replay every slot it
	// owed.
	schedulerCatchUpWindow = 2 * time.Hour

	// schedulerMaxJitter caps the deterministic spread applied to a slot.
	schedulerMaxJitter = 5 * time.Minute

	// snapshotRunTimeout is how long a dispatched run may stay unfinished before
	// the sweeper fails it. Pairs with skip-on-overrun: skipping without a
	// timeout turns one stuck run into an indefinite outage that looks like
	// working config.
	snapshotRunTimeout = 2 * time.Hour
)

// databaseScheduler owns *when* a scheduled snapshot happens.
//
// Scheduling used to live in the runner's memory: the control plane sent a cron
// expression and the worker fired it. That model cannot answer the only question
// that matters — "did the 03:00 run happen?" — because a schedule that never
// fires emits nothing, and a cron in a process that restarts has no memory of
// what it owed. It also had no way to stagger tenants or refuse to overlap.
//
// Here, every slot becomes a row before anything is dispatched. The unique index
// on (instance, nominal slot) is the concurrency control, so the database
// arbitrates rather than a lock, and a duplicate claim loses harmlessly.
type databaseScheduler struct {
	workerHub *socket.WorkerHub
	snapshots routers.SnapshotStarter

	// jitterSeed spreads instances that share a cron expression. Deterministic
	// rather than random: random jitter fixes the thundering herd but destroys
	// predictability, so an operator can never say when a backup will run.
	// Prometheus uses exactly this (hash XOR a per-server seed) for scrape
	// offsets, and the per-deployment component is what stops staging and
	// production colliding on a shared destination.
	jitterSeed uint64

	stop chan struct{}
}

var dbScheduler *databaseScheduler

func newDatabaseScheduler(hub *socket.WorkerHub, snapshots routers.SnapshotStarter) *databaseScheduler {
	return &databaseScheduler{
		workerHub:  hub,
		snapshots:  snapshots,
		jitterSeed: deploymentJitterSeed(),
		stop:       make(chan struct{}),
	}
}

// deploymentJitterSeed derives a stable per-deployment seed. Rotating it
// re-spreads the whole fleet, which is occasionally what you want.
func deploymentJitterSeed() uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("lattice-snapshot-scheduler"))
	_, _ = h.Write([]byte(Version))
	return h.Sum64()
}

func (s *databaseScheduler) Start() {
	go s.runLoop("db-scheduler", schedulerTick, s.dispatchDue)
	go s.runLoop("db-run-timeout", time.Minute, s.failStuckRuns)
	logger.Info("database", "snapshot scheduler started", logger.F{
		"tick":            schedulerTick.String(),
		"catch_up_window": schedulerCatchUpWindow.String(),
		"max_jitter":      schedulerMaxJitter.String(),
	})
}

func (s *databaseScheduler) Stop() { close(s.stop) }

func (s *databaseScheduler) runLoop(name string, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("panic", fmt.Sprintf("%v", r), logger.F{"goroutine": name})
					}
				}()
				fn()
			}()
		}
	}
}

// jitterFor returns a stable offset for an instance, bounded by both the cap and
// the schedule's own period — a jitter wider than the period would reorder runs.
func (s *databaseScheduler) jitterFor(instanceID int, period time.Duration) time.Duration {
	max := schedulerMaxJitter
	if period > 0 && period < max {
		max = period / 2
	}
	if max <= 0 {
		return 0
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("instance:%d", instanceID)))
	offset := (h.Sum64() ^ s.jitterSeed) % uint64(max/time.Second+1)
	return time.Duration(offset) * time.Second
}

// dispatchDue claims and dispatches every slot that is due.
func (s *databaseScheduler) dispatchDue() {
	instances, _, err := query.ListDatabaseInstances(db.DB, query.ListDatabaseInstancesRequest{
		Limit: db.MAX_LIMIT,
	})
	if err != nil || instances == nil {
		return
	}

	now := time.Now().UTC()

	for i := range *instances {
		instance := (*instances)[i]
		if instance.SnapshotSchedule == nil || *instance.SnapshotSchedule == "" || instance.BackupDestinationID == nil {
			continue
		}

		// The two most recent nominal slots. The newest is the candidate; the
		// gap between them is the period, which bounds jitter.
		fires := cronPreviousFires(*instance.SnapshotSchedule, now, 2, staleBackupLookback)
		if len(fires) == 0 {
			continue
		}
		slot := fires[0]
		period := time.Duration(0)
		if len(fires) > 1 {
			period = fires[0].Sub(fires[1])
		}

		// Hold the slot until its jittered moment, so instances sharing a cron
		// expression do not all start at once. A destination that throttles under
		// a synchronised burst fails *every* backup and produces N alerts for one
		// cause; staggered, it degrades.
		if now.Before(slot.Add(s.jitterFor(instance.ID, period))) {
			continue
		}

		s.dispatchSlot(instance, slot, now)
	}
}

func (s *databaseScheduler) dispatchSlot(instance structs.DatabaseInstance, slot, now time.Time) {
	run, won, err := query.ClaimSnapshotRun(db.DB, instance.ID, slot)
	if err != nil {
		logger.Error("database", "failed to claim snapshot slot", logger.F{
			"database_instance_id": instance.ID, "scheduled_at": slot, "error": err,
		})
		return
	}
	if !won {
		return // already claimed — the unique index did its job
	}

	skip := func(reason string) {
		status := string(structs.SnapshotRunSkipped)
		_ = query.UpdateSnapshotRun(db.DB, run.ID, query.UpdateSnapshotRunRequest{
			Status: &status, SkipReason: &reason, Finished: true,
		})
		logger.Warn("database", "scheduled snapshot skipped", logger.F{
			"database_instance_id": instance.ID,
			"scheduled_at":         slot,
			"reason":               reason,
		})
	}

	// Too old to be worth running. Recorded, not silently dropped.
	if now.Sub(slot) > schedulerCatchUpWindow {
		skip(fmt.Sprintf("slot was %s old; beyond the %s catch-up window",
			now.Sub(slot).Round(time.Minute), schedulerCatchUpWindow))
		return
	}

	// Skip on overrun, never queue and never kill the running one. Killing a
	// 90%-complete backup to start one that will also overrun guarantees you
	// never complete a backup; queueing turns an overrun into an unbounded
	// backlog. Skipping is self-limiting.
	inFlight, err := query.HasRunInFlight(db.DB, instance.ID)
	if err == nil && inFlight {
		skip("the previous scheduled snapshot is still running")
		return
	}

	if instance.Status != string(structs.DBStatusRunning) {
		skip(fmt.Sprintf("database is %s", instance.Status))
		return
	}
	if !s.workerHub.IsConnected(instance.WorkerID) {
		skip(fmt.Sprintf("worker %d is not connected", instance.WorkerID))
		return
	}

	snapshot, err := s.snapshots.StartSnapshot(&instance, "scheduled")
	if err != nil {
		status := string(structs.SnapshotRunFailed)
		reason := err.Error()
		_ = query.UpdateSnapshotRun(db.DB, run.ID, query.UpdateSnapshotRunRequest{
			Status: &status, SkipReason: &reason, Finished: true,
		})
		logger.Error("database", "failed to dispatch scheduled snapshot", logger.F{
			"database_instance_id": instance.ID, "error": err,
		})
		return
	}

	status := string(structs.SnapshotRunRunning)
	dispatched := time.Now().UTC()
	_ = query.UpdateSnapshotRun(db.DB, run.ID, query.UpdateSnapshotRunRequest{
		Status: &status, SnapshotID: &snapshot.ID, DispatchedAt: &dispatched,
	})

	logger.Info("database", "scheduled snapshot dispatched", logger.F{
		"database_instance_id": instance.ID,
		"scheduled_at":         slot,
		"snapshot_id":          snapshot.ID,
	})
}

// failStuckRuns closes out runs that were dispatched and never reported back.
//
// Without this, skip-on-overrun becomes an indefinite outage dressed as working
// config: one run that never finishes blocks every subsequent slot forever.
func (s *databaseScheduler) failStuckRuns() {
	cutoff := time.Now().UTC().Add(-snapshotRunTimeout)
	runs, err := query.ListStuckSnapshotRuns(db.DB, cutoff)
	if err != nil {
		return
	}
	for _, run := range runs {
		status := string(structs.SnapshotRunFailed)
		reason := fmt.Sprintf("no result within %s of its scheduled time", snapshotRunTimeout)
		_ = query.UpdateSnapshotRun(db.DB, run.ID, query.UpdateSnapshotRunRequest{
			Status: &status, SkipReason: &reason, Finished: true,
		})
		logger.Warn("database", "scheduled snapshot run timed out", logger.F{
			"database_instance_id": run.DatabaseInstanceID,
			"scheduled_at":         run.ScheduledAt,
		})
	}
}

// closeRunForSnapshot marks the run owning a snapshot finished. Called when a
// snapshot reaches a terminal status.
func closeRunForSnapshot(snapshotID int, succeeded bool) {
	run, err := query.FindRunBySnapshotID(db.DB, snapshotID)
	if err != nil {
		return
	}
	status := string(structs.SnapshotRunDone)
	if !succeeded {
		status = string(structs.SnapshotRunFailed)
	}
	_ = query.UpdateSnapshotRun(db.DB, run.ID, query.UpdateSnapshotRunRequest{
		Status: &status, Finished: true,
	})
}
