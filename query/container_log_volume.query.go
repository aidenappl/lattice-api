package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

// Log volume report bounds. Every row in the window is read (LENGTH(message)
// needs the row), so the window is short; container_logs keeps 7 days anyway.
const (
	LOG_VOLUME_DEFAULT_DAYS = 1
	LOG_VOLUME_MAX_DAYS     = 7
)

type ContainerLogVolumeRequest struct {
	Days  int // whole UTC days back, including today
	Limit int
	Now   time.Time
}

// ContainerLogVolume is bytes and lines per container per UTC day, largest
// first. A day partition is only read when it is in the window.
func ContainerLogVolume(engine db.Queryable, req ContainerLogVolumeRequest) (*[]structs.ContainerLogVolume, error) {
	q, args, err := containerLogVolumeQuery(req)
	if err != nil {
		return nil, err
	}
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	out := []structs.ContainerLogVolume{}
	for rows.Next() {
		var v structs.ContainerLogVolume
		if err := rows.Scan(&v.Day, &v.ContainerID, &v.ContainerName, &v.StackID, &v.Lines, &v.Bytes); err != nil {
			return nil, fmt.Errorf("failed to scan log volume: %w", err)
		}
		out = append(out, v)
	}
	return &out, rows.Err()
}

func containerLogVolumeQuery(req ContainerLogVolumeRequest) (string, []any, error) {
	if req.Days <= 0 || req.Days > LOG_VOLUME_MAX_DAYS {
		req.Days = LOG_VOLUME_DEFAULT_DAYS
	}
	if req.Limit <= 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	n := req.Now.UTC()
	since := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(req.Days - 1))

	q := sq.Select(
		"DATE_FORMAT(container_logs.recorded_at, '%Y-%m-%d') AS day",
		"container_logs.container_id",
		"MAX(container_logs.container_name)",
		"MAX(containers.stack_id)",
		"COUNT(*) AS line_count",
		"COALESCE(SUM(LENGTH(container_logs.message)), 0) AS byte_count",
	).From("container_logs").
		LeftJoin("containers ON containers.id = container_logs.container_id").
		Where(sq.GtOrEq{"container_logs.recorded_at": since.Format("2006-01-02 15:04:05")}).
		// Unmanaged containers have no id: group them by name instead.
		GroupBy("day", "container_logs.container_id", "CASE WHEN container_logs.container_id IS NULL THEN container_logs.container_name END").
		OrderBy("byte_count DESC").
		Limit(uint64(req.Limit))
	qStr, args, err := q.ToSql()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build sql query: %w", err)
	}
	return qStr, args, nil
}
