package query

import (
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var metricsColumns = []string{
	"worker_metrics.id",
	"worker_metrics.worker_id",
	"worker_metrics.cpu_percent",
	"worker_metrics.cpu_cores",
	"worker_metrics.load_avg_1",
	"worker_metrics.load_avg_5",
	"worker_metrics.load_avg_15",
	"worker_metrics.memory_used_mb",
	"worker_metrics.memory_total_mb",
	"worker_metrics.memory_free_mb",
	"worker_metrics.swap_used_mb",
	"worker_metrics.swap_total_mb",
	"worker_metrics.disk_used_mb",
	"worker_metrics.disk_total_mb",
	"worker_metrics.container_count",
	"worker_metrics.container_running_count",
	"worker_metrics.network_rx_bytes",
	"worker_metrics.network_tx_bytes",
	"worker_metrics.uptime_seconds",
	"worker_metrics.process_count",
	"worker_metrics.recorded_at",
}

func scanMetrics(row scanner) (*structs.WorkerMetrics, error) {
	var m structs.WorkerMetrics
	err := row.Scan(
		&m.ID,
		&m.WorkerID,
		&m.CPUPercent,
		&m.CPUCores,
		&m.LoadAvg1,
		&m.LoadAvg5,
		&m.LoadAvg15,
		&m.MemoryUsedMB,
		&m.MemoryTotalMB,
		&m.MemoryFreeMB,
		&m.SwapUsedMB,
		&m.SwapTotalMB,
		&m.DiskUsedMB,
		&m.DiskTotalMB,
		&m.ContainerCount,
		&m.ContainerRunningCount,
		&m.NetworkRxBytes,
		&m.NetworkTxBytes,
		&m.UptimeSeconds,
		&m.ProcessCount,
		&m.RecordedAt,
	)
	return &m, err
}

func GetLatestMetrics(engine db.Queryable, workerID int) (*structs.WorkerMetrics, error) {
	q := sq.Select(metricsColumns...).
		From("worker_metrics").
		Where(sq.Eq{"worker_metrics.worker_id": workerID}).
		OrderBy("worker_metrics.recorded_at DESC").
		Limit(1)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	m, err := scanMetrics(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan metrics: %w", err)
	}

	return m, nil
}

type ListMetricsRequest struct {
	WorkerID int
	Limit    int
	Since    *time.Time
}

func ListMetrics(engine db.Queryable, req ListMetricsRequest) (*[]structs.WorkerMetrics, error) {
	q := sq.Select(metricsColumns...).
		From("worker_metrics").
		Where(sq.Eq{"worker_metrics.worker_id": req.WorkerID}).
		OrderBy("worker_metrics.recorded_at DESC")

	if req.Since != nil {
		q = q.Where(sq.GtOrEq{"worker_metrics.recorded_at": *req.Since})
	}
	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var metrics []structs.WorkerMetrics
	for rows.Next() {
		m, err := scanMetrics(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan metrics: %w", err)
		}
		metrics = append(metrics, *m)
	}

	return &metrics, rows.Err()
}

type CreateMetricsRequest struct {
	WorkerID              int
	CPUPercent            *float64
	CPUCores              *int
	LoadAvg1              *float64
	LoadAvg5              *float64
	LoadAvg15             *float64
	MemoryUsedMB          *float64
	MemoryTotalMB         *float64
	MemoryFreeMB          *float64
	SwapUsedMB            *float64
	SwapTotalMB           *float64
	DiskUsedMB            *float64
	DiskTotalMB           *float64
	ContainerCount        *int
	ContainerRunningCount *int
	NetworkRxBytes        *int64
	NetworkTxBytes        *int64
	UptimeSeconds         *float64
	ProcessCount          *int
}

func CreateMetrics(engine db.Queryable, req CreateMetricsRequest) error {
	q := sq.Insert("worker_metrics").
		Columns("worker_id", "cpu_percent", "cpu_cores",
			"load_avg_1", "load_avg_5", "load_avg_15",
			"memory_used_mb", "memory_total_mb", "memory_free_mb",
			"swap_used_mb", "swap_total_mb",
			"disk_used_mb", "disk_total_mb",
			"container_count", "container_running_count",
			"network_rx_bytes", "network_tx_bytes",
			"uptime_seconds", "process_count").
		Values(req.WorkerID, req.CPUPercent, req.CPUCores,
			req.LoadAvg1, req.LoadAvg5, req.LoadAvg15,
			req.MemoryUsedMB, req.MemoryTotalMB, req.MemoryFreeMB,
			req.SwapUsedMB, req.SwapTotalMB,
			req.DiskUsedMB, req.DiskTotalMB,
			req.ContainerCount, req.ContainerRunningCount,
			req.NetworkRxBytes, req.NetworkTxBytes,
			req.UptimeSeconds, req.ProcessCount)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

// GetLatestMetricsForAllWorkers returns the most recent metrics row for each worker
// that has a heartbeat within the last 2 minutes (i.e., online workers).
func GetLatestMetricsForAllWorkers(engine db.Queryable) ([]structs.WorkerMetrics, error) {
	rawSQL := `
		SELECT ` + joinColumns(metricsColumns) + `
		FROM worker_metrics
		INNER JOIN (
			SELECT worker_id, MAX(recorded_at) AS max_recorded
			FROM worker_metrics
			WHERE recorded_at >= ?
			GROUP BY worker_id
		) latest ON worker_metrics.worker_id = latest.worker_id
		           AND worker_metrics.recorded_at = latest.max_recorded
	`
	cutoff := time.Now().Add(-2 * time.Minute)
	rows, err := engine.Query(rawSQL, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query fleet metrics: %w", err)
	}
	defer rows.Close()

	var metrics []structs.WorkerMetrics
	for rows.Next() {
		m, err := scanMetrics(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan fleet metrics: %w", err)
		}
		metrics = append(metrics, *m)
	}
	return metrics, rows.Err()
}

// FleetMetricsPoint represents a single aggregated fleet-wide metrics snapshot.
type FleetMetricsPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	CPUAvg         float64   `json:"cpu_avg"`
	MemoryAvg      float64   `json:"memory_avg"`
	NetworkRxTotal int64     `json:"network_rx_total"`
	NetworkTxTotal int64     `json:"network_tx_total"`
	ContainerCount int       `json:"container_count"`
	RunningCount   int       `json:"running_count"`
}

// GetFleetMetricsHistory returns aggregated fleet metrics sampled at intervals.
// It groups all metrics within each interval bucket and averages CPU/memory,
// sums network bytes and container counts across workers.
func GetFleetMetricsHistory(engine db.Queryable, since time.Time, points int) ([]FleetMetricsPoint, error) {
	duration := time.Since(since)
	interval := duration / time.Duration(points)
	if interval < time.Minute {
		interval = time.Minute
	}

	// Get raw metrics since the cutoff, ordered by time.
	// Include worker_id so we can aggregate per-worker then sum across the fleet.
	rawSQL := `
		SELECT worker_id, recorded_at, cpu_percent, memory_used_mb, memory_total_mb,
		       network_rx_bytes, network_tx_bytes, container_count, container_running_count
		FROM worker_metrics
		WHERE recorded_at >= ?
		ORDER BY recorded_at ASC
	`
	rows, err := engine.Query(rawSQL, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query fleet history: %w", err)
	}
	defer rows.Close()

	type rawRow struct {
		workerID      int
		recordedAt    time.Time
		cpuPercent    *float64
		memUsed       *float64
		memTotal      *float64
		netRx         *int64
		netTx         *int64
		containerCnt  *int
		runningCnt    *int
	}

	var rawRows []rawRow
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.workerID, &r.recordedAt, &r.cpuPercent, &r.memUsed, &r.memTotal,
			&r.netRx, &r.netTx, &r.containerCnt, &r.runningCnt); err != nil {
			return nil, fmt.Errorf("failed to scan fleet history: %w", err)
		}
		rawRows = append(rawRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(rawRows) == 0 {
		return []FleetMetricsPoint{}, nil
	}

	// Bucket into intervals.
	// Within each bucket, aggregate per-worker first (average each worker's
	// heartbeats), then combine across workers: average CPU/memory, sum
	// containers/network. This produces fleet-wide totals that match the
	// real-time WebSocket aggregation.
	result := make([]FleetMetricsPoint, 0, points)
	bucketStart := since
	ri := 0

	type workerBucket struct {
		cpuSum, memSum     float64
		cpuN, memN         int
		netRx, netTx       int64
		containerCnt       int
		runningCnt         int
		rows               int
	}

	for i := 0; i < points; i++ {
		bucketEnd := bucketStart.Add(interval)
		workers := make(map[int]*workerBucket)

		for ri < len(rawRows) && rawRows[ri].recordedAt.Before(bucketEnd) {
			r := rawRows[ri]
			wb, ok := workers[r.workerID]
			if !ok {
				wb = &workerBucket{}
				workers[r.workerID] = wb
			}
			if r.cpuPercent != nil {
				wb.cpuSum += *r.cpuPercent
				wb.cpuN++
			}
			if r.memUsed != nil && r.memTotal != nil && *r.memTotal > 0 {
				wb.memSum += (*r.memUsed / *r.memTotal) * 100
				wb.memN++
			}
			if r.netRx != nil {
				wb.netRx = *r.netRx // use latest value per worker
			}
			if r.netTx != nil {
				wb.netTx = *r.netTx
			}
			if r.containerCnt != nil {
				wb.containerCnt = *r.containerCnt // use latest
			}
			if r.runningCnt != nil {
				wb.runningCnt = *r.runningCnt // use latest
			}
			wb.rows++
			ri++
		}

		if len(workers) > 0 {
			var cpuTotal, memTotal float64
			var cpuWorkers, memWorkers int
			var netRxTotal, netTxTotal int64
			var containerTotal, runningTotal int

			for _, wb := range workers {
				if wb.cpuN > 0 {
					cpuTotal += wb.cpuSum / float64(wb.cpuN) // per-worker avg
					cpuWorkers++
				}
				if wb.memN > 0 {
					memTotal += wb.memSum / float64(wb.memN) // per-worker avg
					memWorkers++
				}
				netRxTotal += wb.netRx
				netTxTotal += wb.netTx
				containerTotal += wb.containerCnt // sum across fleet
				runningTotal += wb.runningCnt     // sum across fleet
			}

			pt := FleetMetricsPoint{
				Timestamp:      bucketStart.Add(interval / 2),
				NetworkRxTotal: netRxTotal,
				NetworkTxTotal: netTxTotal,
				ContainerCount: containerTotal,
				RunningCount:   runningTotal,
			}
			if cpuWorkers > 0 {
				pt.CPUAvg = cpuTotal / float64(cpuWorkers) // fleet average
			}
			if memWorkers > 0 {
				pt.MemoryAvg = memTotal / float64(memWorkers) // fleet average
			}
			result = append(result, pt)
		}

		bucketStart = bucketEnd
	}

	return result, nil
}

func joinColumns(cols []string) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c)
	}
	return b.String()
}
