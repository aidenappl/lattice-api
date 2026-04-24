package query

import (
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var lifecycleLogColumns = []string{
	"lifecycle_logs.id",
	"lifecycle_logs.container_id",
	"lifecycle_logs.container_name",
	"lifecycle_logs.worker_id",
	"lifecycle_logs.event",
	"lifecycle_logs.message",
	"lifecycle_logs.recorded_at",
}

func scanLifecycleLog(row scanner) (*structs.LifecycleLog, error) {
	var l structs.LifecycleLog
	err := row.Scan(
		&l.ID,
		&l.ContainerID,
		&l.ContainerName,
		&l.WorkerID,
		&l.Event,
		&l.Message,
		&l.RecordedAt,
	)
	return &l, err
}

type ListLifecycleLogsRequest struct {
	Limit         int
	Offset        int
	ContainerID   *int
	ContainerName *string
	WorkerID      *int
}

func ListLifecycleLogs(engine db.Queryable, req ListLifecycleLogsRequest) (*[]structs.LifecycleLog, error) {
	q := sq.Select(lifecycleLogColumns...).From("lifecycle_logs")

	if req.ContainerID != nil {
		if req.ContainerName != nil {
			q = q.Where(sq.Or{
				sq.Eq{"lifecycle_logs.container_id": *req.ContainerID},
				sq.And{
					sq.Expr("lifecycle_logs.container_id IS NULL"),
					sq.Eq{"lifecycle_logs.container_name": *req.ContainerName},
				},
			})
		} else {
			q = q.Where(sq.Eq{"lifecycle_logs.container_id": *req.ContainerID})
		}
	}
	if req.WorkerID != nil {
		q = q.Where(sq.Eq{"lifecycle_logs.worker_id": *req.WorkerID})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("lifecycle_logs.recorded_at DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var logs []structs.LifecycleLog
	for rows.Next() {
		l, err := scanLifecycleLog(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lifecycle log: %w", err)
		}
		logs = append(logs, *l)
	}

	return &logs, rows.Err()
}

type CreateLifecycleLogRequest struct {
	ContainerID   *int
	ContainerName *string
	WorkerID      int
	Event         string
	Message       string
}

func CreateLifecycleLog(engine db.Queryable, req CreateLifecycleLogRequest) error {
	// Use a timestamp slightly in the past so the lifecycle entry sorts
	// before the new container's first log lines (which the runner may
	// have already timestamped by the time the API processes the event).
	ts := time.Now().Add(-1 * time.Second).UTC().Format("2006-01-02 15:04:05.999999")

	q := sq.Insert("lifecycle_logs").
		Columns("container_id", "container_name", "worker_id", "event", "message", "recorded_at").
		Values(req.ContainerID, req.ContainerName, req.WorkerID, req.Event, req.Message, ts)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	// INSERT IGNORE discards rows that violate the unique index on
	// (container_id, worker_id, event, recorded_at), preventing message
	// replays from creating duplicate lifecycle entries.
	qStr = strings.Replace(qStr, "INSERT INTO", "INSERT IGNORE INTO", 1)

	_, err = engine.Exec(qStr, args...)
	return err
}
