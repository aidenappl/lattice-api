package query

import (
	"fmt"
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
