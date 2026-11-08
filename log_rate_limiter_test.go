package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

func logLine(containerID int) query.CreateContainerLogRequest {
	name := "web"
	return query.CreateContainerLogRequest{ContainerID: &containerID, ContainerName: &name, WorkerID: 3, Stream: "stdout", Message: "hello"}
}

func TestLogRateLimiterCap(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		limit       int
		lines       int
		wantStored  int
		wantDropped int
	}{
		{"under the limit", 5, 3, 3, 0},
		{"at the limit", 5, 5, 5, 0},
		{"over the limit", 5, 12, 5, 7},
		{"unlimited", structs.LOG_LINES_UNLIMITED, 50, 50, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLogRateLimiter()
			stored := 0
			for i := 0; i < tt.lines; i++ {
				ok, marker := l.Allow(logLine(7), tt.limit, t0.Add(time.Duration(i)*time.Second))
				if marker != nil {
					t.Fatalf("marker inside the first window: %+v", marker)
				}
				if ok {
					stored++
				}
			}
			if stored != tt.wantStored {
				t.Errorf("stored %d, want %d", stored, tt.wantStored)
			}
			markers := l.Sweep(t0.Add(2 * time.Minute))
			if tt.wantDropped == 0 {
				if len(markers) != 0 {
					t.Errorf("unexpected markers %+v", markers)
				}
				return
			}
			if len(markers) != 1 {
				t.Fatalf("got %d markers, want 1", len(markers))
			}
			m := markers[0]
			if !strings.Contains(m.Message, "7 log lines dropped") || !strings.Contains(m.Message, "limit of 5 lines") {
				t.Errorf("marker message %q", m.Message)
			}
			if m.ContainerID == nil || *m.ContainerID != 7 || m.WorkerID != 3 || m.Stream != "stderr" {
				t.Errorf("marker identity %+v", m)
			}
			if m.RecordedAt == nil || !m.RecordedAt.Equal(t0.Add(time.Minute)) {
				t.Errorf("marker recorded_at %v, want the window end", m.RecordedAt)
			}
		})
	}
}

func TestLogRateLimiterNextWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := newLogRateLimiter()
	for i := 0; i < 4; i++ {
		l.Allow(logLine(1), 2, t0)
	}
	// Another container has its own budget.
	if ok, _ := l.Allow(logLine(2), 2, t0); !ok {
		t.Fatal("a second container was capped by the first's window")
	}
	// The first line of the next window is stored, preceded by the marker.
	ok, marker := l.Allow(logLine(1), 2, t0.Add(61*time.Second))
	if !ok {
		t.Fatal("first line of a new window was dropped")
	}
	if marker == nil || !strings.Contains(marker.Message, "2 log lines dropped") {
		t.Fatalf("marker = %+v, want 2 dropped", marker)
	}
	// That window's marker was already emitted, so a sweep doesn't repeat it.
	for _, m := range l.Sweep(t0.Add(5 * time.Minute)) {
		if m.ContainerID != nil && *m.ContainerID == 1 {
			t.Errorf("marker emitted twice: %+v", m)
		}
	}
}

func TestLogRateKey(t *testing.T) {
	name := "x"
	id := 4
	cases := map[string]query.CreateContainerLogRequest{
		"id:4":     {ContainerID: &id, ContainerName: &name, WorkerID: 1},
		"name:1:x": {ContainerName: &name, WorkerID: 1},
		"worker:1": {WorkerID: 1},
	}
	for want, r := range cases {
		if got := logRateKey(r); got != want {
			t.Errorf("logRateKey = %q, want %q", got, want)
		}
	}
}

func TestStackLogLimits(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	var fail bool
	s := newStackLogLimits(func(id int) (int, error) {
		calls++
		if fail {
			return 0, errors.New("db down")
		}
		return 50, nil
	})
	if got := s.For(0, t0); got != structs.LOG_LINES_DEFAULT {
		t.Errorf("unmanaged container limit %d, want the default", got)
	}
	if got := s.For(9, t0); got != 50 || calls != 1 {
		t.Errorf("For = %d after %d fetches", got, calls)
	}
	if got := s.For(9, t0.Add(30*time.Second)); got != 50 || calls != 1 {
		t.Errorf("cached For = %d after %d fetches, want no refetch", got, calls)
	}
	fail = true
	if got := s.For(9, t0.Add(2*time.Minute)); got != 50 {
		t.Errorf("a failed refetch should keep the last limit, got %d", got)
	}
	if got := s.For(10, t0); got != structs.LOG_LINES_DEFAULT {
		t.Errorf("unreadable stack limit %d, want the default", got)
	}
}
