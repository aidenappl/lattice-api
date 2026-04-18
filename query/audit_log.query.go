package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var auditColumns = []string{
	"audit_log.id",
	"audit_log.user_id",
	"audit_log.action",
	"audit_log.resource_type",
	"audit_log.resource_id",
	"audit_log.details",
	"audit_log.ip_address",
	"audit_log.inserted_at",
}

func scanAuditEntry(row scanner) (*structs.AuditLogEntry, error) {
	var a structs.AuditLogEntry
	err := row.Scan(
		&a.ID,
		&a.UserID,
		&a.Action,
		&a.ResourceType,
		&a.ResourceID,
		&a.Details,
		&a.IPAddress,
		&a.InsertedAt,
	)
	return &a, err
}

type ListAuditLogRequest struct {
	Limit        int
	Offset       int
	UserID       *int
	Action       *string
	ResourceType *string
}

func ListAuditLog(engine db.Queryable, req ListAuditLogRequest) (*[]structs.AuditLogEntry, error) {
	q := sq.Select(auditColumns...).From("audit_log")

	if req.UserID != nil {
		q = q.Where(sq.Eq{"audit_log.user_id": *req.UserID})
	}
	if req.Action != nil {
		q = q.Where(sq.Eq{"audit_log.action": *req.Action})
	}
	if req.ResourceType != nil {
		q = q.Where(sq.Eq{"audit_log.resource_type": *req.ResourceType})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("audit_log.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var entries []structs.AuditLogEntry
	for rows.Next() {
		a, err := scanAuditEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan audit entry: %w", err)
		}
		entries = append(entries, *a)
	}

	return &entries, rows.Err()
}

type CreateAuditLogRequest struct {
	UserID       *int
	Action       string
	ResourceType string
	ResourceID   *int
	Details      *string
	IPAddress    *string
}

func CreateAuditLog(engine db.Queryable, req CreateAuditLogRequest) error {
	q := sq.Insert("audit_log").
		Columns("user_id", "action", "resource_type", "resource_id", "details", "ip_address").
		Values(req.UserID, req.Action, req.ResourceType, req.ResourceID, req.Details, req.IPAddress)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}
