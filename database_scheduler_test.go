package main

import (
	"testing"
	"time"
)

func testScheduler() *databaseScheduler {
	return &databaseScheduler{jitterSeed: 0xdeadbeefcafe}
}

// Jitter must be deterministic. Random jitter fixes the thundering herd but
// destroys predictability — an operator could never say when a backup will run,
// and a restart would move every schedule.
func TestJitterIsDeterministic(t *testing.T) {
	s := testScheduler()
	period := time.Hour

	first := s.jitterFor(42, period)
	for i := 0; i < 100; i++ {
		if got := s.jitterFor(42, period); got != first {
			t.Fatalf("jitter changed between calls: %s then %s", first, got)
		}
	}
}

// Different instances sharing one cron expression must not all fire together: a
// destination that throttles under a synchronised burst fails every backup at
// once and produces N alerts for a single cause.
func TestJitterSpreadsInstances(t *testing.T) {
	s := testScheduler()
	seen := map[time.Duration]int{}
	for id := 1; id <= 40; id++ {
		seen[s.jitterFor(id, time.Hour)]++
	}
	if len(seen) < 10 {
		t.Errorf("40 instances produced only %d distinct offsets — too clustered to stagger anything", len(seen))
	}
}

// A jitter wider than the schedule's own period would reorder runs.
func TestJitterNeverExceedsHalfThePeriod(t *testing.T) {
	s := testScheduler()
	period := 2 * time.Minute
	for id := 1; id <= 200; id++ {
		if j := s.jitterFor(id, period); j > period/2 {
			t.Fatalf("instance %d got jitter %s for a %s period", id, j, period)
		}
	}
}

func TestJitterIsBoundedByTheCap(t *testing.T) {
	s := testScheduler()
	for id := 1; id <= 200; id++ {
		if j := s.jitterFor(id, 24*time.Hour); j > schedulerMaxJitter {
			t.Fatalf("instance %d exceeded the cap: %s > %s", id, j, schedulerMaxJitter)
		}
	}
}

// The per-deployment seed is what stops staging and production — same instance
// ids, same crons — colliding on a shared backup destination.
func TestSeedChangesTheSpread(t *testing.T) {
	a := &databaseScheduler{jitterSeed: 1}
	b := &databaseScheduler{jitterSeed: 2}

	differences := 0
	for id := 1; id <= 50; id++ {
		if a.jitterFor(id, time.Hour) != b.jitterFor(id, time.Hour) {
			differences++
		}
	}
	if differences == 0 {
		t.Error("both deployments produced identical offsets for every instance")
	}
}

// Catch-up is bounded on purpose. A control plane that was down overnight should
// take one backup on recovery, not replay every slot it owed — the unbounded
// version is how Kubernetes CronJobs wedge after 100 missed schedules.
func TestCatchUpWindowIsBounded(t *testing.T) {
	if schedulerCatchUpWindow <= 0 || schedulerCatchUpWindow > 24*time.Hour {
		t.Errorf("catch-up window %s is not a sane bound", schedulerCatchUpWindow)
	}
	slot := time.Now().UTC().Add(-schedulerCatchUpWindow - time.Minute)
	if time.Since(slot) <= schedulerCatchUpWindow {
		t.Error("a slot older than the window should be outside it")
	}
}

// Skip-on-overrun without a timeout turns one stuck run into an indefinite
// outage that looks like working configuration.
func TestRunTimeoutExistsToUnblockOverrunSkipping(t *testing.T) {
	if snapshotRunTimeout <= 0 {
		t.Fatal("a dispatched run must eventually time out, or skip-on-overrun blocks every later slot forever")
	}
	if snapshotRunTimeout < schedulerCatchUpWindow {
		t.Errorf("run timeout (%s) shorter than the catch-up window (%s) would fail runs that are merely late",
			snapshotRunTimeout, schedulerCatchUpWindow)
	}
}
