package query

import (
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/structs"
)

func TestMetricsSince(t *testing.T) {
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	at := func(mins int) structs.WorkerMetrics {
		return structs.WorkerMetrics{RecordedAt: base.Add(time.Duration(mins) * time.Minute)}
	}
	newestFirst := []structs.WorkerMetrics{at(0), at(-10), at(-30), at(-61), at(-90)}

	tests := []struct {
		name  string
		since time.Time
		want  int
	}{
		{"keeps samples inside the window", base.Add(-time.Hour), 3},
		{"a sample exactly at since is kept", base.Add(-30 * time.Minute), 3},
		{"window covering everything", base.Add(-24 * time.Hour), 5},
		{"window after the newest sample", base.Add(time.Minute), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metricsSince(newestFirst, tt.since); len(got) != tt.want {
				t.Fatalf("got %d samples, want %d", len(got), tt.want)
			}
		})
	}

	if got := metricsSince(nil, base); len(got) != 0 {
		t.Fatalf("nil input: got %d samples", len(got))
	}
}
