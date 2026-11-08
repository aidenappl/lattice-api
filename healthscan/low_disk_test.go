package healthscan

import (
	"strings"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/hostdisk"
	"github.com/aidenappl/lattice-api/structs"
)

func TestLowDiskAnomaly(t *testing.T) {
	now := time.Now()
	f := func(v float64) *float64 { return &v }
	sample := func(usedMB, totalMB float64, age time.Duration) *structs.WorkerMetrics {
		return &structs.WorkerMetrics{DiskUsedMB: f(usedMB), DiskTotalMB: f(totalMB), RecordedAt: now.Add(-age)}
	}
	cases := []struct {
		name string
		m    *structs.WorkerMetrics
		want bool
	}{
		{"plenty free", sample(30_000, 61_000, 0), false},
		{"under 10 percent", sample(56_000, 61_000, 0), true},
		{"over 10 percent but under 5 GB", sample(36_000, 40_000, 0), true},
		{"large disk, 8 percent but 80 GB free", sample(920_000, 1_000_000, 0), true},
		{"full", sample(61_000, 61_000, 0), true},
		{"stale sample ignored", sample(61_000, 61_000, 10*time.Minute), false},
		{"no sample", nil, false},
		{"no disk fields", &structs.WorkerMetrics{RecordedAt: now}, false},
		{"zero total", sample(0, 0, 0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lowDiskAnomaly(6, "worker-6", c.m, now)
			if (got != nil) != c.want {
				t.Fatalf("lowDiskAnomaly flagged=%v, want %v", got != nil, c.want)
			}
			if got != nil && (got.Type != AnomalyLowDisk || got.WorkerID != 6) {
				t.Errorf("unexpected anomaly %+v", got)
			}
		})
	}
}

func TestControlPlaneLowDiskAnomalies(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	samples := []hostdisk.Sample{
		{Label: "root", Path: "/", TotalMB: 62464, FreeMB: 1024, FreePct: 1.6, Low: true, At: at},
		{Label: "compose", Path: "/compose", TotalMB: 62464, FreeMB: 30000, FreePct: 48, Low: false, At: at},
		{Label: "mariadb", Path: "/var/lib/mysql/", At: at, Error: "no such container"},
	}
	got := controlPlaneLowDiskAnomalies(samples)
	if len(got) != 1 {
		t.Fatalf("got %d anomalies, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Type != AnomalyControlPlaneLowDisk || a.ID != "0:control_plane_low_disk:root" || a.WorkerID != 0 || a.WorkerName != "control-plane" {
		t.Errorf("anomaly identity %+v", a)
	}
	if a.Details["label"] != "root" || a.Details["threshold_free_pct"] != LOW_DISK_FREE_PCT {
		t.Errorf("details %+v", a.Details)
	}
	if !strings.Contains(a.Message, "1.0 GB free") {
		t.Errorf("message %q", a.Message)
	}
	if got := controlPlaneLowDiskAnomalies(nil); len(got) != 0 {
		t.Errorf("no samples gave %+v", got)
	}
}
