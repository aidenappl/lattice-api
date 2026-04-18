package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var eventColumns = []string{
	"container_events.id",
	"container_events.container_id",
	"container_events.worker_id",
	"container_events.event_type",
	"container_events.message",
	"container_events.recorded_at",
}

func scanEvent(row scanner) (*structs.ContainerEvent, error) {
	var e structs.ContainerEvent
	err := row.Scan(
		&e.ID,
		&e.ContainerID,
		&e.WorkerID,
		&e.EventType,
		&e.Message,
		&e.RecordedAt,
	)
	return &e, err
}

type ListEventsRequest struct {
	Limit       int
	WorkerID    *int
	ContainerID *int
}

func ListContainerEvents(engine db.Queryable, req ListEventsRequest) (*[]structs.ContainerEvent, error) {
	q := sq.Select(eventColumns...).From("container_events")

	if req.WorkerID != nil {
		q = q.Where(sq.Eq{"container_events.worker_id": *req.WorkerID})
	}
	if req.ContainerID != nil {
		q = q.Where(sq.Eq{"container_events.container_id": *req.ContainerID})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))

	q = q.OrderBy("container_events.recorded_at DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var events []structs.ContainerEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, *e)
	}

	return &events, rows.Err()
}

type CreateContainerEventRequest struct {
	ContainerID *int
	WorkerID    int
	EventType   string
	Message     *string
}

func CreateContainerEvent(engine db.Queryable, req CreateContainerEventRequest) error {
	q := sq.Insert("container_events").
		Columns("container_id", "worker_id", "event_type", "message").
		Values(req.ContainerID, req.WorkerID, req.EventType, req.Message)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}
