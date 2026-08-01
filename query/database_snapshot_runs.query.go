package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var databaseSnapshotRunColumns = []string{
	"database_snapshot_runs.id",
	"database_snapshot_runs.database_instance_id",
	"database_snapshot_runs.scheduled_at",
	"database_snapshot_runs.status",
	"database_snapshot_runs.skip_reason",
	"database_snapshot_runs.snapshot_id",
	"database_snapshot_runs.dispatched_at",
	"database_snapshot_runs.finished_at",
	"database_snapshot_runs.inserted_at",
	"database_snapshot_runs.updated_at",
}

func scanSnapshotRun(row scanner) (*structs.DatabaseSnapshotRun, error) {
	var r structs.DatabaseSnapshotRun
	err := row.Scan(
		&r.ID,
		&r.DatabaseInstanceID,
		&r.ScheduledAt,
		&r.Status,
		&r.SkipReason,
		&r.SnapshotID,
		&r.DispatchedAt,
		&r.FinishedAt,
		&r.InsertedAt,
		&r.UpdatedAt,
	)
	return &r, err
}

// ClaimSnapshotRun inserts the run row for a nominal slot and reports whether
// this caller won it.
//
// The unique index on (database_instance_id, scheduled_at) *is* the concurrency
// control: a second attempt at the same slot loses on insert. That is why no
// lock, no leader election and no SKIP LOCKED is required for correctness — the
// database arbitrates, and a duplicate is a no-op rather than a double backup.
//
// Returns (nil, false, nil) when the slot was already claimed.
func ClaimSnapshotRun(engine db.Queryable, instanceID int, scheduledAt time.Time) (*structs.DatabaseSnapshotRun, bool, error) {
	q := sq.Insert("database_snapshot_runs").
		Columns("database_instance_id", "scheduled_at", "status").
		Values(instanceID, scheduledAt.UTC(), structs.SnapshotRunClaimed)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, false, fmt.Errorf("failed to build sql query: %w", err)
	}

	result, err := engine.Exec(qStr, args...)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to claim snapshot run: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, false, fmt.Errorf("failed to read claimed run id: %w", err)
	}

	run, err := GetSnapshotRunByID(engine, int(id))
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func GetSnapshotRunByID(engine db.Queryable, id int) (*structs.DatabaseSnapshotRun, error) {
	q := sq.Select(databaseSnapshotRunColumns...).
		From("database_snapshot_runs").
		Where(sq.Eq{"database_snapshot_runs.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	run, err := scanSnapshotRun(engine.QueryRow(qStr, args...))
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan snapshot run: %w", err)
	}
	return run, nil
}

// HasRunInFlight reports whether an instance has an *other* scheduled run that
// has not finished. Used to skip rather than overlap.
//
// excludeRunID is required, not optional, and that is the point: the caller has
// necessarily just claimed a run of its own, and that row is itself `claimed`.
// Counting it made every scheduled run skip itself with "the previous scheduled
// snapshot is still running" — a self-inflicted deadlock that no unit test here
// caught, because the bug is in the *sequence* (claim, then check) rather than
// in either step. Making the parameter mandatory means the mistake cannot be
// repeated by omission.
func HasRunInFlight(engine db.Queryable, instanceID, excludeRunID int) (bool, error) {
	q := sq.Select("COUNT(*)").
		From("database_snapshot_runs").
		Where(sq.Eq{"database_snapshot_runs.database_instance_id": instanceID}).
		Where(sq.NotEq{"database_snapshot_runs.id": excludeRunID}).
		Where(sq.Eq{"database_snapshot_runs.status": []string{
			string(structs.SnapshotRunClaimed), string(structs.SnapshotRunRunning),
		}})

	qStr, args, err := q.ToSql()
	if err != nil {
		return false, fmt.Errorf("failed to build sql query: %w", err)
	}

	var count int
	if err := engine.QueryRow(qStr, args...).Scan(&count); err != nil {
		return false, fmt.Errorf("failed to count in-flight runs: %w", err)
	}
	return count > 0, nil
}

type UpdateSnapshotRunRequest struct {
	Status       *string
	SkipReason   *string
	SnapshotID   *int
	DispatchedAt *time.Time
	Finished     bool
}

func UpdateSnapshotRun(engine db.Queryable, id int, req UpdateSnapshotRunRequest) error {
	q := sq.Update("database_snapshot_runs").Where(sq.Eq{"id": id})
	has := false

	if req.Status != nil {
		q = q.Set("status", *req.Status)
		has = true
	}
	if req.SkipReason != nil {
		q = q.Set("skip_reason", *req.SkipReason)
		has = true
	}
	if req.SnapshotID != nil {
		q = q.Set("snapshot_id", *req.SnapshotID)
		has = true
	}
	if req.DispatchedAt != nil {
		q = q.Set("dispatched_at", *req.DispatchedAt)
		has = true
	}
	if req.Finished {
		q = q.Set("finished_at", time.Now().UTC())
		has = true
	}
	if !has {
		return nil
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}
	_, err = engine.Exec(qStr, args...)
	return err
}

// FindRunBySnapshotID resolves the run a snapshot belongs to, so a snapshot's
// terminal status can close its run.
func FindRunBySnapshotID(engine db.Queryable, snapshotID int) (*structs.DatabaseSnapshotRun, error) {
	q := sq.Select(databaseSnapshotRunColumns...).
		From("database_snapshot_runs").
		Where(sq.Eq{"database_snapshot_runs.snapshot_id": snapshotID}).
		OrderBy("database_snapshot_runs.id DESC").
		Limit(1)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	run, err := scanSnapshotRun(engine.QueryRow(qStr, args...))
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan snapshot run: %w", err)
	}
	return run, nil
}

// ListStuckSnapshotRuns returns runs dispatched before cutoff that never
// finished — a dispatched command whose worker never replied.
func ListStuckSnapshotRuns(engine db.Queryable, cutoff time.Time) ([]structs.DatabaseSnapshotRun, error) {
	q := sq.Select(databaseSnapshotRunColumns...).
		From("database_snapshot_runs").
		Where(sq.Eq{"database_snapshot_runs.status": []string{
			string(structs.SnapshotRunClaimed), string(structs.SnapshotRunRunning),
		}}).
		Where(sq.Lt{"database_snapshot_runs.scheduled_at": cutoff}).
		Limit(uint64(db.MAX_LIMIT))

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var runs []structs.DatabaseSnapshotRun
	for rows.Next() {
		r, err := scanSnapshotRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan snapshot run: %w", err)
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}

// ListSnapshotRunsByInstance returns recent runs, newest first — including
// skipped ones, which is the point.
func ListSnapshotRunsByInstance(engine db.Queryable, instanceID, limit int) ([]structs.DatabaseSnapshotRun, error) {
	if limit <= 0 || limit > db.MAX_LIMIT {
		limit = db.DEFAULT_LIMIT
	}
	q := sq.Select(databaseSnapshotRunColumns...).
		From("database_snapshot_runs").
		Where(sq.Eq{"database_snapshot_runs.database_instance_id": instanceID}).
		OrderBy("database_snapshot_runs.scheduled_at DESC").
		Limit(uint64(limit))

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var runs []structs.DatabaseSnapshotRun
	for rows.Next() {
		r, err := scanSnapshotRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan snapshot run: %w", err)
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}
