package main

import (
	"testing"
	"time"
)

func TestCronMatches(t *testing.T) {
	tests := []struct {
		name string
		expr string
		at   time.Time
		want bool
	}{
		{"every minute", "* * * * *", time.Date(2026, 7, 30, 3, 17, 0, 0, time.UTC), true},
		{"daily at 03:00 — on time", "0 3 * * *", time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), true},
		{"daily at 03:00 — wrong minute", "0 3 * * *", time.Date(2026, 7, 30, 3, 1, 0, 0, time.UTC), false},
		{"daily at 03:00 — wrong hour", "0 3 * * *", time.Date(2026, 7, 30, 4, 0, 0, 0, time.UTC), false},
		{"every five minutes — aligned", "*/5 * * * *", time.Date(2026, 7, 30, 3, 15, 0, 0, time.UTC), true},
		{"every five minutes — unaligned", "*/5 * * * *", time.Date(2026, 7, 30, 3, 17, 0, 0, time.UTC), false},
		{"weekday range — Thursday", "0 3 * * 1-5", time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), true},
		{"weekday range — Sunday", "0 3 * * 1-5", time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC), false},
		{"list of hours — match", "0 3,15 * * *", time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC), true},
		{"list of hours — miss", "0 3,15 * * *", time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC), false},
		{"monthly on the 1st", "0 3 1 * *", time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC), true},
		{"monthly on the 1st — wrong day", "0 3 1 * *", time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC), false},
		{"malformed expression never matches", "0 3 * *", time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), false},
		{"garbage field never matches", "0 abc * * *", time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronMatches(tt.expr, tt.at); got != tt.want {
				t.Errorf("cronMatches(%q, %s) = %v, want %v", tt.expr, tt.at.Format(time.RFC3339), got, tt.want)
			}
		})
	}
}

func TestCronPreviousFires(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 30, 0, 0, time.UTC)

	t.Run("daily schedule yields the last two 03:00s", func(t *testing.T) {
		fires := cronPreviousFires("0 3 * * *", now, 2, staleBackupLookback)
		if len(fires) != 2 {
			t.Fatalf("got %d fires, want 2", len(fires))
		}
		want0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
		want1 := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
		if !fires[0].Equal(want0) || !fires[1].Equal(want1) {
			t.Errorf("got %v, want [%v %v]", fires, want0, want1)
		}
	})

	t.Run("newest first", func(t *testing.T) {
		fires := cronPreviousFires("*/5 * * * *", now, 3, time.Hour)
		for i := 1; i < len(fires); i++ {
			if !fires[i].Before(fires[i-1]) {
				t.Errorf("fires not ordered newest-first: %v", fires)
			}
		}
	})

	t.Run("a monthly schedule is still found within the lookback", func(t *testing.T) {
		// The reason the lookback is 70 days: a monthly schedule needs two fires
		// to be judged, which spans more than one month.
		fires := cronPreviousFires("0 3 1 * *", now, 2, staleBackupLookback)
		if len(fires) != 2 {
			t.Fatalf("monthly schedule yielded %d fires, want 2 — staleness would never be evaluated", len(fires))
		}
	})

	t.Run("a narrow lookback yields fewer fires, which callers must tolerate", func(t *testing.T) {
		fires := cronPreviousFires("0 3 * * *", now, 2, 12*time.Hour)
		if len(fires) != 1 {
			t.Fatalf("got %d fires, want 1", len(fires))
		}
	})

	t.Run("malformed expression yields nothing rather than looping", func(t *testing.T) {
		if fires := cronPreviousFires("nonsense", now, 2, staleBackupLookback); fires != nil {
			t.Errorf("expected no fires for a malformed expression, got %v", fires)
		}
	})
}

// TestStalenessRuleUsesSecondMostRecentFire documents the judgement the alarm
// makes: one missed run is tolerated, two is not.
func TestStalenessRuleUsesSecondMostRecentFire(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	fires := cronPreviousFires("0 3 * * *", now, 2, staleBackupLookback)
	if len(fires) != 2 {
		t.Fatalf("expected 2 fires")
	}
	deadline := fires[1] // 2026-07-29 03:00

	oneMissed := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC) // after yesterday's run
	twoMissed := time.Date(2026, 7, 28, 3, 5, 0, 0, time.UTC) // before it

	if oneMissed.Before(deadline) {
		t.Error("a single missed run should not be flagged stale")
	}
	if !twoMissed.Before(deadline) {
		t.Error("two consecutive missed runs should be flagged stale")
	}
}
