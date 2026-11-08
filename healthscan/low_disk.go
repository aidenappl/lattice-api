package healthscan

import (
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/hostdisk"
	"github.com/aidenappl/lattice-api/structs"
)

// AnomalyLowDisk is a worker whose root filesystem is nearly full. Images,
// container logs, managed databases and the Monitor spool share that disk, so
// at 100% a database is at risk and telemetry is dropped (APL-86: worker-6
// filled in one day of deploys).
const AnomalyLowDisk AnomalyType = "low_disk"

// AnomalyControlPlaneLowDisk is the control-plane host itself (lattice-api,
// lattice-web, MariaDB) nearly full, from hostdisk's samples (APL-121). It is
// computed when anomalies are read, not by the 5-minute scan, so it still
// shows when the database the scan reads is what the full disk broke.
const AnomalyControlPlaneLowDisk AnomalyType = "control_plane_low_disk"

// Low-disk thresholds: a worker or a control-plane path is flagged under
// either. They are hostdisk's; lattice-web's src/lib/disk.ts colours the disk
// gauges red at the same values, so change both together.
const (
	LOW_DISK_FREE_PCT = hostdisk.LowFreePct
	LOW_DISK_FREE_MB  = hostdisk.LowFreeMB
)

// controlPlaneLowDiskAnomalies is one anomaly per low control-plane sample.
// Worker 0 and the name "control-plane" mark them as not a worker's.
func controlPlaneLowDiskAnomalies(samples []hostdisk.Sample) []Anomaly {
	var out []Anomaly
	for _, s := range samples {
		if s.Error != "" || !s.Low {
			continue
		}
		out = append(out, Anomaly{
			ID:         anomalyID(0, string(AnomalyControlPlaneLowDisk), s.Label),
			Type:       AnomalyControlPlaneLowDisk,
			WorkerName: "control-plane",
			Message:    fmt.Sprintf("Control-plane disk nearly full (%s, %s): %.1f GB free (%.1f%%) of %.1f GB", s.Label, s.Path, s.FreeMB/1024, s.FreePct, s.TotalMB/1024),
			DetectedAt: s.At,
			Details: map[string]any{
				"label": s.Label, "path": s.Path,
				"disk_free_mb": s.FreeMB, "disk_total_mb": s.TotalMB, "disk_free_pct": s.FreePct,
				"threshold_free_pct": LOW_DISK_FREE_PCT, "threshold_free_mb": LOW_DISK_FREE_MB,
			},
		})
	}
	return out
}

// lowDiskFreshness bounds how old a metrics sample may be and still count.
// Workers send one every heartbeat.
const lowDiskFreshness = 5 * time.Minute

// lowDiskAnomaly flags a worker whose latest fresh sample shows less than
// LOW_DISK_FREE_PCT or LOW_DISK_FREE_MB free. Nil without a usable sample.
func lowDiskAnomaly(workerID int, workerName string, m *structs.WorkerMetrics, now time.Time) *Anomaly {
	if m == nil || m.DiskUsedMB == nil || m.DiskTotalMB == nil || *m.DiskTotalMB <= 0 {
		return nil
	}
	if now.Sub(m.RecordedAt) > lowDiskFreshness {
		return nil
	}
	total, used := *m.DiskTotalMB, *m.DiskUsedMB
	freeMB := total - used
	if freeMB < 0 {
		freeMB = 0
	}
	freePct := freeMB / total * 100
	if freePct >= LOW_DISK_FREE_PCT && freeMB >= LOW_DISK_FREE_MB {
		return nil
	}
	return &Anomaly{
		ID:         anomalyID(workerID, string(AnomalyLowDisk), ""),
		Type:       AnomalyLowDisk,
		WorkerID:   workerID,
		WorkerName: workerName,
		Message:    fmt.Sprintf("Worker disk nearly full: %.1f GB free (%.1f%%) of %.1f GB", freeMB/1024, freePct, total/1024),
		DetectedAt: now,
		Details: map[string]any{
			"disk_free_mb": freeMB, "disk_total_mb": total, "disk_free_pct": freePct,
			"threshold_free_pct": LOW_DISK_FREE_PCT, "threshold_free_mb": LOW_DISK_FREE_MB,
		},
	}
}
