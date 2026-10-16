package query

import (
	"fmt"
	"strconv"
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
	"worker_metrics.network_rx_rate",
	"worker_metrics.network_tx_rate",
	"worker_metrics.uptime_seconds",
	"worker_metrics.process_count",
	"worker_metrics.runner_goroutines",
	"worker_metrics.runner_heap_mb",
	"worker_metrics.runner_sys_mb",
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
		&m.NetworkRxRate,
		&m.NetworkTxRate,
		&m.UptimeSeconds,
		&m.ProcessCount,
		&m.RunnerGoroutines,
		&m.RunnerHeapMB,
		&m.RunnerSysMB,
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

// ListMetrics returns a worker's newest samples, newest first, optionally only
// those recorded at or after Since.
//
// Since is applied in Go, not in SQL. With `AND recorded_at >= ?` in the WHERE
// clause the production database picked a plan that took 2–4 minutes (every
// ranged call from the worker metrics page timed out), while the same query
// without it returns 500 rows in under a second. Because rows come back
// ordered by recorded_at, the newest Limit rows filtered to >= Since are
// exactly the newest Limit rows that satisfy it, so the result is unchanged.
func ListMetrics(engine db.Queryable, req ListMetricsRequest) (*[]structs.WorkerMetrics, error) {
	q := sq.Select(metricsColumns...).
		From("worker_metrics").
		Where(sq.Eq{"worker_metrics.worker_id": req.WorkerID}).
		OrderBy("worker_metrics.recorded_at DESC")

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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if req.Since != nil {
		metrics = metricsSince(metrics, *req.Since)
	}
	return &metrics, nil
}

// metricsSince keeps the leading run of newest-first samples recorded at or
// after since.
func metricsSince(newestFirst []structs.WorkerMetrics, since time.Time) []structs.WorkerMetrics {
	for i, m := range newestFirst {
		if m.RecordedAt.Before(since) {
			return newestFirst[:i]
		}
	}
	return newestFirst
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
	NetworkRxRate         *float64
	NetworkTxRate         *float64
	UptimeSeconds         *float64
	ProcessCount          *int
	RunnerGoroutines      *int
	RunnerHeapMB          *float64
	RunnerSysMB           *float64
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
			"network_rx_rate", "network_tx_rate",
			"uptime_seconds", "process_count",
			"runner_goroutines", "runner_heap_mb", "runner_sys_mb").
		Values(req.WorkerID, req.CPUPercent, req.CPUCores,
			req.LoadAvg1, req.LoadAvg5, req.LoadAvg15,
			req.MemoryUsedMB, req.MemoryTotalMB, req.MemoryFreeMB,
			req.SwapUsedMB, req.SwapTotalMB,
			req.DiskUsedMB, req.DiskTotalMB,
			req.ContainerCount, req.ContainerRunningCount,
			req.NetworkRxBytes, req.NetworkTxBytes,
			req.NetworkRxRate, req.NetworkTxRate,
			req.UptimeSeconds, req.ProcessCount,
			req.RunnerGoroutines, req.RunnerHeapMB, req.RunnerSysMB)

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
	NetworkRxRate  float64   `json:"network_rx_rate"`
	NetworkTxRate  float64   `json:"network_tx_rate"`
	ContainerCount int       `json:"container_count"`
	RunningCount   int       `json:"running_count"`
	OnlineWorkers  int       `json:"online_workers"`
}

// GetFleetMetricsHistory returns aggregated fleet metrics bucketed by time interval.
// Aggregation is done in SQL: each bucket averages CPU/memory per worker first,
// then averages across workers for fleet-wide values. Network uses pre-computed
// rates (bytes/sec) instead of cumulative byte counters.
//
// The window and bucket size are inlined as integers (see windowClause) rather
// than bound as parameters.
func GetFleetMetricsHistory(engine db.Queryable, since time.Time, points int) ([]FleetMetricsPoint, error) {
	windowSec := int(time.Since(since).Seconds())
	intervalSec := bucketSeconds(windowSec, points, 60)

	// Two-level aggregation in SQL:
	// Inner query: per-worker per-bucket averages
	// Outer query: fleet-wide averages across workers per bucket
	rawSQL := `
		SELECT
			bucket_ts,
			AVG(worker_cpu_avg) AS cpu_avg,
			AVG(worker_mem_avg) AS memory_avg,
			SUM(worker_rx_rate) AS network_rx_rate,
			SUM(worker_tx_rate) AS network_tx_rate,
			SUM(worker_containers) AS container_count,
			SUM(worker_running) AS running_count,
			COUNT(*) AS online_workers
		FROM (
			SELECT
				worker_id,
				FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(recorded_at) / ` + strconv.Itoa(intervalSec) + `) * ` + strconv.Itoa(intervalSec) + `) AS bucket_ts,
				AVG(cpu_percent) AS worker_cpu_avg,
				AVG(CASE WHEN memory_total_mb > 0 THEN (memory_used_mb / memory_total_mb) * 100 ELSE NULL END) AS worker_mem_avg,
				AVG(COALESCE(network_rx_rate, 0)) AS worker_rx_rate,
				AVG(COALESCE(network_tx_rate, 0)) AS worker_tx_rate,
				MAX(container_count) AS worker_containers,
				MAX(container_running_count) AS worker_running
			FROM worker_metrics
			WHERE ` + windowClause(windowSec) + `
			GROUP BY worker_id, bucket_ts
		) per_worker
		GROUP BY bucket_ts
		ORDER BY bucket_ts ASC
	`

	rows, err := engine.Query(rawSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to query fleet history: %w", err)
	}
	defer rows.Close()

	var result []FleetMetricsPoint
	for rows.Next() {
		var pt FleetMetricsPoint
		var cpuAvg, memAvg, rxRate, txRate *float64
		var containerCnt, runningCnt, onlineWorkers *int

		if err := rows.Scan(&pt.Timestamp, &cpuAvg, &memAvg, &rxRate, &txRate,
			&containerCnt, &runningCnt, &onlineWorkers); err != nil {
			return nil, fmt.Errorf("failed to scan fleet history: %w", err)
		}
		if cpuAvg != nil {
			pt.CPUAvg = *cpuAvg
		}
		if memAvg != nil {
			pt.MemoryAvg = *memAvg
		}
		if rxRate != nil {
			pt.NetworkRxRate = *rxRate
		}
		if txRate != nil {
			pt.NetworkTxRate = *txRate
		}
		if containerCnt != nil {
			pt.ContainerCount = *containerCnt
		}
		if runningCnt != nil {
			pt.RunningCount = *runningCnt
		}
		if onlineWorkers != nil {
			pt.OnlineWorkers = *onlineWorkers
		}
		result = append(result, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if result == nil {
		return []FleetMetricsPoint{}, nil
	}
	return result, nil
}

// windowClause is the recorded_at filter for "the last windowSec seconds",
// computed in SQL from an inlined integer.
//
// Binding the start time as a parameter (`recorded_at >= ?`) made the
// production database full-scan worker_metrics (2.2M rows, ~6.5 s for every
// fleet-metrics range, minutes for the old ranged worker query); SHOW EXPLAIN
// on the live query showed type ALL with idx_wm_recorded available. The same
// query with the window computed in SQL is an index range read of a few
// thousand rows (~30 ms). The integer comes from server-side constants, never
// from request input, so inlining it is safe.
func windowClause(windowSec int) string {
	if windowSec < 1 {
		windowSec = 1
	}
	return "recorded_at >= NOW() - INTERVAL " + strconv.Itoa(windowSec) + " SECOND"
}

// bucketSeconds splits a window into about `points` buckets, never smaller
// than minSec.
func bucketSeconds(windowSec, points, minSec int) int {
	if points < 1 {
		points = 1
	}
	sec := windowSec / points
	if sec < minSec {
		sec = minSec
	}
	return sec
}

// workerBucketColumns aggregates one bucket of a worker's samples into the
// metricsColumns shape so scanMetrics can read it: gauges are averaged,
// capacities and cumulative counters take the max, and recorded_at is the
// bucket's newest sample.
var workerBucketColumns = []string{
	"MAX(id)",
	"MAX(worker_id)",
	"AVG(cpu_percent)",
	"MAX(cpu_cores)",
	"AVG(load_avg_1)",
	"AVG(load_avg_5)",
	"AVG(load_avg_15)",
	"AVG(memory_used_mb)",
	"MAX(memory_total_mb)",
	"AVG(memory_free_mb)",
	"AVG(swap_used_mb)",
	"MAX(swap_total_mb)",
	"AVG(disk_used_mb)",
	"MAX(disk_total_mb)",
	"CAST(ROUND(AVG(container_count)) AS SIGNED)",
	"CAST(ROUND(AVG(container_running_count)) AS SIGNED)",
	"MAX(network_rx_bytes)",
	"MAX(network_tx_bytes)",
	"AVG(network_rx_rate)",
	"AVG(network_tx_rate)",
	"MAX(uptime_seconds)",
	"CAST(ROUND(AVG(process_count)) AS SIGNED)",
	"CAST(ROUND(AVG(runner_goroutines)) AS SIGNED)",
	"AVG(runner_heap_mb)",
	"AVG(runner_sys_mb)",
	"MAX(recorded_at)",
}

// ListMetricsBucketed returns a worker's samples over the last `window`,
// aggregated into about `points` time buckets, newest first.
//
// Ranged worker charts used to take the newest 500 raw samples. At one sample
// every 10 s that is ~80 minutes, so the 24h and 7d views silently showed only
// the last hour or so. Bucketing covers the whole window at a fixed size.
func ListMetricsBucketed(engine db.Queryable, workerID int, window time.Duration, points int) ([]structs.WorkerMetrics, error) {
	windowSec := int(window.Seconds())
	intervalSec := bucketSeconds(windowSec, points, 10)

	rawSQL := `
		SELECT ` + strings.Join(workerBucketColumns, ", ") + `
		FROM worker_metrics
		WHERE worker_id = ` + strconv.Itoa(workerID) + ` AND ` + windowClause(windowSec) + `
		GROUP BY FLOOR(UNIX_TIMESTAMP(recorded_at) / ` + strconv.Itoa(intervalSec) + `)
		ORDER BY MAX(recorded_at) DESC
	`
	rows, err := engine.Query(rawSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to query bucketed worker metrics: %w", err)
	}
	defer rows.Close()

	metrics := []structs.WorkerMetrics{}
	for rows.Next() {
		m, err := scanMetrics(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bucketed worker metrics: %w", err)
		}
		metrics = append(metrics, *m)
	}
	return metrics, rows.Err()
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
