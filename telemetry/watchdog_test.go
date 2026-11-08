package telemetry

import (
	"testing"
	"time"

	monitor "github.com/aidenappl/go-monitor"
)

func TestWatchdogObserve(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	type step struct {
		at                  time.Duration
		st                  monitor.ShipperStats
		wantStalled, wantOK bool // transition flags: became stalled, recovered
		wantState           bool
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"delivering is never a stall", []step{
			{0, monitor.ShipperStats{Enqueued: 10, Flushed: 10}, false, false, false},
			{10 * time.Minute, monitor.ShipperStats{Enqueued: 20, Flushed: 20}, false, false, false},
		}},
		{"idle is never a stall", []step{
			{0, monitor.ShipperStats{Enqueued: 10, Flushed: 10}, false, false, false},
			{30 * time.Minute, monitor.ShipperStats{Enqueued: 10, Flushed: 10}, false, false, false},
		}},
		{"waiting without progress stalls once, then recovers", []step{
			{0, monitor.ShipperStats{Enqueued: 10, Flushed: 10}, false, false, false},
			{4 * time.Minute, monitor.ShipperStats{Enqueued: 50, Flushed: 10, Spooled: 50, Pending: 40}, false, false, false},
			{5 * time.Minute, monitor.ShipperStats{Enqueued: 60, Flushed: 10, Spooled: 60, Pending: 50}, true, false, true},
			{6 * time.Minute, monitor.ShipperStats{Enqueued: 70, Flushed: 10, Spooled: 70, Pending: 60}, false, false, true},
			{9 * time.Minute, monitor.ShipperStats{Enqueued: 80, Flushed: 80}, false, true, false},
		}},
		{"memory-only backlog counts as waiting", []step{
			{0, monitor.ShipperStats{Enqueued: 5, Flushed: 5}, false, false, false},
			{6 * time.Minute, monitor.ShipperStats{Enqueued: 40, Flushed: 5}, true, false, true},
		}},
		{"dropped events are not waiting", []step{
			{0, monitor.ShipperStats{Enqueued: 5, Flushed: 5}, false, false, false},
			{6 * time.Minute, monitor.ShipperStats{Enqueued: 40, Flushed: 5, Dropped: 35}, false, false, false},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &watchdog{}
			for i, s := range tt.steps {
				stalled, recovered, _ := w.observe(t0.Add(s.at), s.st)
				if stalled != s.wantStalled || recovered != s.wantOK {
					t.Errorf("step %d: stalled=%v recovered=%v, want %v %v", i, stalled, recovered, s.wantStalled, s.wantOK)
				}
				if got := w.state().Stalled; got != s.wantState {
					t.Errorf("step %d: state.Stalled=%v, want %v", i, got, s.wantState)
				}
			}
		})
	}
}

func TestWatchdogStateReportsWhenItStalled(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 8, 3, 34, 0, time.UTC)
	w := &watchdog{}
	w.observe(t0, monitor.ShipperStats{Enqueued: 1, Flushed: 1})
	w.observe(t0.Add(STALL_AFTER), monitor.ShipperStats{Enqueued: 9, Flushed: 1, Pending: 8})
	s := w.state()
	if !s.Stalled || s.StalledSince == nil || !s.StalledSince.Equal(t0) {
		t.Fatalf("state = %+v, want stalled since %v", s, t0)
	}
}
