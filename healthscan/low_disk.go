package healthscan

import (
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/structs"
)

// AnomalyLowDisk is a worker whose root filesystem is nearly full. Images,
// container logs, managed databases and the Monitor spool share that disk, so
// at 100% a database is at risk and telemetry is dropped (APL-86: worker-6
// filled in one day of deploys).
const AnomalyLowDisk AnomalyType = "low_disk"

// Low-disk thresholds: a worker is flagged under either. lattice-web's
// src/lib/disk.ts colours the worker disk gauge red at the same values, so
// change both together.
const (
	LOW_DISK_FREE_PCT = 10.0
	LOW_DISK_FREE_MB  = 5 * 1024.0
)

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
