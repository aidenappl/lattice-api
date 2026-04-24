package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var containerMetricsColumns = []string{
	"container_metrics.id",
	"container_metrics.worker_id",
	"container_metrics.container_id",
	"container_metrics.container_name",
	"container_metrics.cpu_percent",
	"container_metrics.mem_usage_mb",
	"container_metrics.mem_limit_mb",
	"container_metrics.mem_percent",
	"container_metrics.recorded_at",
}

func scanContainerMetrics(row scanner) (*structs.ContainerMetrics, error) {
	var m structs.ContainerMetrics
	err := row.Scan(
		&m.ID,
		&m.WorkerID,
		&m.ContainerID,
		&m.ContainerName,
		&m.CPUPercent,
		&m.MemUsageMB,
		&m.MemLimitMB,
		&m.MemPercent,
		&m.RecordedAt,
	)
	return &m, err
}

type CreateContainerMetricsRequest struct {
	WorkerID      int
	ContainerID   *int
	ContainerName string
	CPUPercent    *float64
	MemUsageMB    *float64
	MemLimitMB    *float64
	MemPercent    *float64
}

func CreateContainerMetrics(engine db.Queryable, req CreateContainerMetricsRequest) error {
	q := sq.Insert("container_metrics").
		Columns("worker_id", "container_id", "container_name",
			"cpu_percent", "mem_usage_mb", "mem_limit_mb", "mem_percent").
		Values(req.WorkerID, req.ContainerID, req.ContainerName,
			req.CPUPercent, req.MemUsageMB, req.MemLimitMB, req.MemPercent)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

// CreateContainerMetricsBatch inserts multiple container metrics rows in a single statement.
func CreateContainerMetricsBatch(engine db.Queryable, reqs []CreateContainerMetricsRequest) error {
	if len(reqs) == 0 {
		return nil
	}

	q := sq.Insert("container_metrics").
		Columns("worker_id", "container_id", "container_name",
			"cpu_percent", "mem_usage_mb", "mem_limit_mb", "mem_percent")

	for _, req := range reqs {
		q = q.Values(req.WorkerID, req.ContainerID, req.ContainerName,
			req.CPUPercent, req.MemUsageMB, req.MemLimitMB, req.MemPercent)
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

type ListContainerMetricsRequest struct {
	ContainerID int
	Limit       int
	Since       *time.Time
}

func ListContainerMetrics(engine db.Queryable, req ListContainerMetricsRequest) ([]structs.ContainerMetrics, error) {
	q := sq.Select(containerMetricsColumns...).
		From("container_metrics").
		Where(sq.Eq{"container_metrics.container_id": req.ContainerID}).
		OrderBy("container_metrics.recorded_at DESC")

	if req.Since != nil {
		q = q.Where(sq.GtOrEq{"container_metrics.recorded_at": *req.Since})
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

	var metrics []structs.ContainerMetrics
	for rows.Next() {
		m, err := scanContainerMetrics(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan container metrics: %w", err)
		}
		metrics = append(metrics, *m)
	}
	return metrics, rows.Err()
}

// GetLatestContainerMetrics returns the most recent metrics for a container.
func GetLatestContainerMetrics(engine db.Queryable, containerID int) (*structs.ContainerMetrics, error) {
	q := sq.Select(containerMetricsColumns...).
		From("container_metrics").
		Where(sq.Eq{"container_metrics.container_id": containerID}).
		OrderBy("container_metrics.recorded_at DESC").
		Limit(1)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	return scanContainerMetrics(row)
}
