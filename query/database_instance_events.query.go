package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var databaseInstanceEventColumns = []string{
	"database_instance_events.id",
	"database_instance_events.database_instance_id",
	"database_instance_events.kind",
	"database_instance_events.status",
	"database_instance_events.message",
	"database_instance_events.code",
	"database_instance_events.actor",
	"database_instance_events.recorded_at",
}

type CreateDatabaseInstanceEventRequest struct {
	DatabaseInstanceID int
	Kind               string
	Status             *string
	Message            string
	Code               *string
	Actor              *string
}

// CreateDatabaseInstanceEvent appends one entry to an instance's history.
func CreateDatabaseInstanceEvent(engine db.Queryable, req CreateDatabaseInstanceEventRequest) error {
	if req.DatabaseInstanceID == 0 || req.Kind == "" {
		return fmt.Errorf("database_instance_id and kind are required")
	}

	q := sq.Insert("database_instance_events").
		Columns("database_instance_id", "kind", "status", "message", "code", "actor").
		Values(req.DatabaseInstanceID, req.Kind, req.Status, req.Message, req.Code, req.Actor)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	if _, err := engine.Exec(qStr, args...); err != nil {
		return fmt.Errorf("failed to execute sql query: %w", err)
	}
	return nil
}

type ListDatabaseInstanceEventsRequest struct {
	DatabaseInstanceID int
	Kind               *string
	Limit              int
	Offset             int
}

// ListDatabaseInstanceEvents returns an instance's history, newest first.
func ListDatabaseInstanceEvents(engine db.Queryable, req ListDatabaseInstanceEventsRequest) ([]structs.DatabaseInstanceEvent, int, error) {
	base := sq.Select(databaseInstanceEventColumns...).
		From("database_instance_events").
		Where(sq.Eq{"database_instance_events.database_instance_id": req.DatabaseInstanceID})

	countQ := sq.Select("COUNT(*)").
		From("database_instance_events").
		Where(sq.Eq{"database_instance_events.database_instance_id": req.DatabaseInstanceID})

	if req.Kind != nil {
		base = base.Where(sq.Eq{"database_instance_events.kind": *req.Kind})
		countQ = countQ.Where(sq.Eq{"database_instance_events.kind": *req.Kind})
	}

	countStr, countArgs, err := countQ.ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build count query: %w", err)
	}
	var total int
	if err := engine.QueryRow(countStr, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to execute count query: %w", err)
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	base = base.OrderBy("database_instance_events.id DESC").Limit(uint64(req.Limit))
	if req.Offset > 0 {
		base = base.Offset(uint64(req.Offset))
	}

	qStr, args, err := base.ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var events []structs.DatabaseInstanceEvent
	for rows.Next() {
		var e structs.DatabaseInstanceEvent
		if err := rows.Scan(
			&e.ID,
			&e.DatabaseInstanceID,
			&e.Kind,
			&e.Status,
			&e.Message,
			&e.Code,
			&e.Actor,
			&e.RecordedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan database instance event: %w", err)
		}
		events = append(events, e)
	}

	return events, total, rows.Err()
}
