package telemetry

import (
	"context"
	"sync"
	"time"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/logger"
)

// STALL_AFTER is how long events may wait undelivered, with nothing at all
// delivered, before the shipper counts as stalled.
//
// On 2026-11-08 lattice-api's events stopped reaching Monitor at 08:03Z and
// only resumed when the 21:20Z self-update restarted the process. Ingest was
// healthy for every other service the whole time, and nothing said this one had
// stopped: a shipper that cannot deliver cannot report that it cannot deliver.
// The watchdog says so on stdout (the container log) and on /admin/health,
// neither of which needs Monitor.
const STALL_AFTER = 5 * time.Minute

// WATCHDOG_EVERY is how often the watchdog reads the shipper's counters.
const WATCHDOG_EVERY = time.Minute

// HEARTBEAT_EVERY is how often telemetry.heartbeat is emitted. Its job is to
// be missed: a Monitor alert on "no telemetry.heartbeat from lattice-api in
// 15 minutes" fires on a stall from outside the process. Each one carries the
// shipper's counters, so the backlog a stall left behind shows once it drains.
const HEARTBEAT_EVERY = 5 * time.Minute

// WatchdogState is what /admin/health reports about delivery.
type WatchdogState struct {
	Stalled bool `json:"stalled"`
	// StalledSince is when delivery last made progress, set while stalled.
	StalledSince *time.Time `json:"stalled_since,omitempty"`
	// LastProgressAt is when Flushed last moved (or the watchdog started).
	LastProgressAt time.Time `json:"last_progress_at"`
}

type watchdog struct {
	mu           sync.Mutex
	started      bool
	lastFlushed  int64
	lastProgress time.Time
	stalled      bool
}

var defaultWatchdog = &watchdog{}

// observe folds in one reading and reports a transition: became stalled, or
// recovered (stallFor is how long it lasted).
func (w *watchdog) observe(now time.Time, st monitor.ShipperStats) (becameStalled, recovered bool, stallFor time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		w.started, w.lastFlushed, w.lastProgress = true, st.Flushed, now
		return false, false, 0
	}
	if st.Flushed != w.lastFlushed {
		w.lastFlushed = st.Flushed
		if w.stalled {
			stallFor = now.Sub(w.lastProgress)
			w.stalled = false
			recovered = true
		}
		w.lastProgress = now
		return false, recovered, stallFor
	}
	// Nothing delivered. Only a stall if something is waiting: an idle process
	// delivers nothing because it has nothing to deliver.
	waiting := st.Pending > 0 || st.Enqueued > st.Flushed+st.Dropped
	if waiting && !w.stalled && now.Sub(w.lastProgress) >= STALL_AFTER {
		w.stalled = true
		return true, false, 0
	}
	if !waiting {
		w.lastProgress = now
	}
	return false, false, 0
}

func (w *watchdog) state() WatchdogState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := WatchdogState{Stalled: w.stalled, LastProgressAt: w.lastProgress}
	if w.stalled {
		since := w.lastProgress
		s.StalledSince = &since
	}
	return s
}

// Delivery reports whether events are reaching Monitor, for /admin/health.
func Delivery() WatchdogState { return defaultWatchdog.state() }

// RunWatchdog checks delivery every WATCHDOG_EVERY and emits the heartbeat
// every HEARTBEAT_EVERY, until ctx ends. A stall is logged at error once, when
// it starts; logger writes it to stdout whether or not Monitor can take it.
func RunWatchdog(ctx context.Context) {
	check := time.NewTicker(WATCHDOG_EVERY)
	defer check.Stop()
	beat := time.NewTicker(HEARTBEAT_EVERY)
	defer beat.Stop()
	defaultWatchdog.observe(time.Now(), monitor.Stats())
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			st := monitor.Stats()
			stalled, recovered, lasted := defaultWatchdog.observe(time.Now(), st)
			if stalled {
				logger.Error("telemetry", "Monitor delivery stalled: events are waiting and none has been delivered", logger.F{
					"stalled_for_s": int(STALL_AFTER.Seconds()), "pending": st.Pending, "enqueued": st.Enqueued,
					"flushed": st.Flushed, "dropped": st.Dropped,
				})
			}
			if recovered {
				logger.Warn("telemetry", "Monitor delivery recovered", logger.F{
					"stalled_for_s": int(lasted.Seconds()), "pending": st.Pending, "dropped": st.Dropped,
				})
			}
		case <-beat.C:
			st := monitor.Stats()
			monitor.Info(ctx, "telemetry.heartbeat", map[string]any{
				"enqueued": st.Enqueued, "flushed": st.Flushed, "dropped": st.Dropped,
				"quarantined": st.Quarantined, "pending": st.Pending, "pending_bytes": st.PendingBytes,
			})
		}
	}
}
