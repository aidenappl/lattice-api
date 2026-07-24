package query

import (
	"database/sql"
	"errors"
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
