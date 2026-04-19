package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var logColumns = []string{
	"container_logs.id",
	"container_logs.container_id",
	"container_logs.container_name",
	"container_logs.worker_id",
	"container_logs.stream",
	"container_logs.message",
	"container_logs.recorded_at",
}

func scanLog(row scanner) (*structs.ContainerLog, error) {
	var l structs.ContainerLog
	err := row.Scan(
		&l.ID,
		&l.ContainerID,
		&l.ContainerName,
		&l.WorkerID,
		&l.Stream,
		&l.Message,
		&l.RecordedAt,
	)
	return &l, err
}

type ListLogsRequest struct {
	Limit       int
	Offset      int
	WorkerID    *int
	ContainerID *int
	Stream      *string
}

func ListContainerLogs(engine db.Queryable, req ListLogsRequest) (*[]structs.ContainerLog, error) {
	q := sq.Select(logColumns...).From("container_logs")

	if req.WorkerID != nil {
		q = q.Where(sq.Eq{"container_logs.worker_id": *req.WorkerID})
	}
	if req.ContainerID != nil {
		q = q.Where(sq.Eq{"container_logs.container_id": *req.ContainerID})
	}
	if req.Stream != nil {
		q = q.Where(sq.Eq{"container_logs.stream": *req.Stream})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("container_logs.recorded_at DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var logs []structs.ContainerLog
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		logs = append(logs, *l)
	}

	return &logs, rows.Err()
}

type CreateContainerLogRequest struct {
	ContainerID   *int
	ContainerName *string
	WorkerID      int
	Stream        string
	Message       string
}

func CreateContainerLog(engine db.Queryable, req CreateContainerLogRequest) error {
	q := sq.Insert("container_logs").
		Columns("container_id", "container_name", "worker_id", "stream", "message").
		Values(req.ContainerID, req.ContainerName, req.WorkerID, req.Stream, req.Message)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}
