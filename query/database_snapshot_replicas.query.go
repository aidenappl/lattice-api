package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var replicaColumns = []string{
	"database_snapshot_replicas.id",
	"database_snapshot_replicas.snapshot_id",
	"database_snapshot_replicas.backup_destination_id",
	"database_snapshot_replicas.role",
	"database_snapshot_replicas.status",
	"database_snapshot_replicas.size_bytes",
	"database_snapshot_replicas.error_message",
	"database_snapshot_replicas.completed_at",
	"database_snapshot_replicas.inserted_at",
	"database_snapshot_replicas.updated_at",
}

func scanReplica(row scanner) (*structs.DatabaseSnapshotReplica, error) {
	var r structs.DatabaseSnapshotReplica
	err := row.Scan(
		&r.ID, &r.SnapshotID, &r.BackupDestinationID, &r.Role, &r.Status,
		&r.SizeBytes, &r.ErrorMessage, &r.CompletedAt, &r.InsertedAt, &r.UpdatedAt,
	)
	return &r, err
}

type UpsertReplicaRequest struct {
	SnapshotID          int
	BackupDestinationID int
	Role                string
	Status              string
	SizeBytes           *int64
	ErrorMessage        *string
}

// UpsertSnapshotReplica records the state of one copy.
//
// Upsert rather than insert: the mirror path writes `pending` when dispatched
// and the terminal state when the worker replies, and a redelivered reply must
// not create a second row for the same (snapshot, destination).
func UpsertSnapshotReplica(engine db.Queryable, req UpsertReplicaRequest) error {
	completedAt := "NULL"
	if req.Status == structs.ReplicaCompleted {
		completedAt = "CURRENT_TIMESTAMP"
	}

	q := fmt.Sprintf(`INSERT INTO database_snapshot_replicas
		(snapshot_id, backup_destination_id, role, status, size_bytes, error_message, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, %s)
		ON DUPLICATE KEY UPDATE
			status = VALUES(status),
			size_bytes = COALESCE(VALUES(size_bytes), size_bytes),
			error_message = VALUES(error_message),
			completed_at = %s`, completedAt, completedAt)

	_, err := engine.Exec(q, req.SnapshotID, req.BackupDestinationID, req.Role, req.Status,
		req.SizeBytes, req.ErrorMessage)
	if err != nil {
		return fmt.Errorf("failed to record snapshot replica: %w", err)
	}
	return nil
}

// ListReplicasBySnapshot returns every copy of a snapshot.
func ListReplicasBySnapshot(engine db.Queryable, snapshotID int) ([]structs.DatabaseSnapshotReplica, error) {
	q := sq.Select(replicaColumns...).
		From("database_snapshot_replicas").
		Where(sq.Eq{"database_snapshot_replicas.snapshot_id": snapshotID}).
		OrderBy("database_snapshot_replicas.role ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var out []structs.DatabaseSnapshotReplica
	for rows.Next() {
		r, err := scanReplica(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan replica: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// FreshReplicasByDestination counts completed copies per destination for an
// instance, within a freshness window.
//
// This is what backup posture counts. Counting *replicas* rather than snapshots
// is the difference between "a snapshot exists" and "a copy exists on that
// destination" — which is the entire question 3-2-1 asks.
func FreshReplicasByDestination(engine db.Queryable, instanceID int, since time.Time) (map[int]int, error) {
	q := sq.Select("r.backup_destination_id", "COUNT(*)").
		From("database_snapshot_replicas r").
		Join("database_snapshots s ON s.id = r.snapshot_id").
		Where(sq.Eq{"s.database_instance_id": instanceID}).
		Where(sq.Eq{"s.active": true}).
		Where(sq.Eq{"r.status": structs.ReplicaCompleted}).
		Where(sq.GtOrEq{"r.completed_at": since}).
		GroupBy("r.backup_destination_id")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var destID, count int
		if err := rows.Scan(&destID, &count); err != nil {
			return nil, fmt.Errorf("failed to scan replica count: %w", err)
		}
		out[destID] = count
	}
	return out, rows.Err()
}
