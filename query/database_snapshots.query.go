package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var databaseSnapshotColumns = []string{
	"database_snapshots.id",
	"database_snapshots.database_instance_id",
	"database_snapshots.backup_destination_id",
	"database_snapshots.filename",
	"database_snapshots.size_bytes",
	"database_snapshots.engine",
	"database_snapshots.database_name",
	"database_snapshots.status",
	"database_snapshots.trigger_type",
	"database_snapshots.error_message",
	"database_snapshots.completed_at",
	"database_snapshots.active",
	"database_snapshots.updated_at",
	"database_snapshots.inserted_at",
}

func scanDatabaseSnapshot(row scanner) (*structs.DatabaseSnapshot, error) {
	var s structs.DatabaseSnapshot
	err := row.Scan(
		&s.ID,
		&s.DatabaseInstanceID,
		&s.BackupDestinationID,
		&s.Filename,
		&s.SizeBytes,
		&s.Engine,
		&s.DatabaseName,
		&s.Status,
		&s.TriggerType,
		&s.ErrorMessage,
		&s.CompletedAt,
		&s.Active,
		&s.UpdatedAt,
		&s.InsertedAt,
	)
	return &s, err
}

func ListSnapshotsByInstance(engine db.Queryable, instanceID int) (*[]structs.DatabaseSnapshot, error) {
	q := sq.Select(databaseSnapshotColumns...).
		From("database_snapshots").
		Where(sq.Eq{"database_snapshots.database_instance_id": instanceID}).
		Where(sq.Eq{"database_snapshots.active": true}).
		OrderBy("database_snapshots.inserted_at DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var snapshots []structs.DatabaseSnapshot
	for rows.Next() {
		s, err := scanDatabaseSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan database snapshot: %w", err)
		}
		snapshots = append(snapshots, *s)
	}

	return &snapshots, rows.Err()
}

func GetSnapshotByID(engine db.Queryable, id int) (*structs.DatabaseSnapshot, error) {
	q := sq.Select(databaseSnapshotColumns...).From("database_snapshots").Where(sq.Eq{"database_snapshots.id": id}).Where(sq.Eq{"database_snapshots.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	s, err := scanDatabaseSnapshot(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan database snapshot: %w", err)
	}

	return s, nil
}

type CreateSnapshotRequest struct {
	DatabaseInstanceID  int
	BackupDestinationID *int
	Filename            string
	Engine              string
	DatabaseName        string
	TriggerType         string
}

func CreateSnapshot(engine db.Queryable, req CreateSnapshotRequest) (*structs.DatabaseSnapshot, error) {
	q := sq.Insert("database_snapshots").
		Columns("database_instance_id", "backup_destination_id", "filename", "engine", "database_name", "trigger_type", "status").
		Values(req.DatabaseInstanceID, req.BackupDestinationID, req.Filename, req.Engine, req.DatabaseName, req.TriggerType, "pending")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	result, err := engine.Exec(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get last insert id: %w", err)
	}

	return GetSnapshotByID(engine, int(id))
}

func UpdateSnapshotStatus(engine db.Queryable, id int, status string, sizeBytes *int64, errorMsg *string) error {
	q := sq.Update("database_snapshots").
		Set("status", status).
		Where(sq.Eq{"id": id})

	if sizeBytes != nil {
		q = q.Set("size_bytes", *sizeBytes)
	}
	if errorMsg != nil {
		q = q.Set("error_message", *errorMsg)
	}
	if status == "completed" {
		q = q.Set("completed_at", time.Now())
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

func CountSnapshotsByInstance(engine db.Queryable, instanceID int) (int, error) {
	q := sq.Select("COUNT(*)").
		From("database_snapshots").
		Where(sq.Eq{"database_snapshots.database_instance_id": instanceID}).
		Where(sq.Eq{"database_snapshots.active": true}).
		Where(sq.Eq{"database_snapshots.status": "completed"})

	qStr, args, err := q.ToSql()
	if err != nil {
		return 0, fmt.Errorf("failed to build sql query: %w", err)
	}

	var count int
	err = engine.QueryRow(qStr, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count snapshots: %w", err)
	}

	return count, nil
}

func DeleteSnapshot(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE database_snapshots SET active = 0 WHERE id = ?", id)
	return err
}
