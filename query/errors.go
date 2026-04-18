package query

import "errors"

var (
	ErrNotFound  = errors.New("resource not found")
	ErrNoChanges = errors.New("no changes applied")
)

type scanner interface {
	Scan(dest ...any) error
}
