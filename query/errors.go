package query

import (
	"database/sql"
	"errors"

	"github.com/go-sql-driver/mysql"
)

// notFoundError is the concrete type behind ErrNotFound. It exposes a
// NotFound() bool method so downstream packages (e.g. responder) can detect a
// not-found condition via a local interface without importing this package —
// avoiding a dependency chain that would pull env/db into leaf packages.
type notFoundError struct{}

func (notFoundError) Error() string  { return "resource not found" }
func (notFoundError) NotFound() bool { return true }

var (
	ErrNotFound  error = notFoundError{}
	ErrNoChanges       = errors.New("no changes applied")
	// ErrNoFreePort means the managed database host-port range is exhausted on
	// the target worker.
	ErrNoFreePort = errors.New("no free port available in the managed range")
)

// isNoRows reports whether err is (or wraps) sql.ErrNoRows. Single-row getters
// use it to translate a missing row into ErrNotFound, which handlers/responder
// map to a 404 instead of a generic 500.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

type scanner interface {
	Scan(dest ...any) error
}

// isDuplicateEntry reports whether err is MySQL's 1062 duplicate-key error.
//
// Losing a unique-key race is not a failure everywhere it appears: the snapshot
// scheduler *uses* the unique index on (database_instance_id, scheduled_at) as
// its concurrency control, so a duplicate means "someone else already claimed
// this slot" — the correct outcome, and the reason no lock or leader election is
// needed for a scheduled run to fire exactly once.
func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return false
}
