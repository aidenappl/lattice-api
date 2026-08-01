package structs

import "time"

// SnapshotRunStatus is the lifecycle of one scheduled snapshot attempt.
//
// `skipped` is a first-class outcome, not an absence. A run that did not happen
// is the thing an operator most needs to see, and recording it only as a log
// line is why "my scheduled backups quietly stopped" is a recurring genre of
// incident rather than an alert.
type SnapshotRunStatus string

const (
	// SnapshotRunClaimed means the slot was won but the command is not yet sent.
	SnapshotRunClaimed SnapshotRunStatus = "claimed"
	// SnapshotRunRunning means the worker has been asked to take the snapshot.
	SnapshotRunRunning SnapshotRunStatus = "running"
	SnapshotRunDone    SnapshotRunStatus = "completed"
	SnapshotRunFailed  SnapshotRunStatus = "failed"
	// SnapshotRunSkipped means the slot was deliberately not run — an overrun,
	// a stopped database, or a slot too old to be worth catching up.
	SnapshotRunSkipped SnapshotRunStatus = "skipped"
)

// DatabaseSnapshotRun is one scheduled attempt, successful or not.
type DatabaseSnapshotRun struct {
	ID                 int `json:"id"`
	DatabaseInstanceID int `json:"database_instance_id"`
	// ScheduledAt is the nominal, un-jittered UTC fire time and the idempotency
	// key. Jitter is applied when dispatching, never here: it must not enter the
	// key, or a restart that recomputes it would mint a second slot for the same
	// logical run.
	ScheduledAt  time.Time  `json:"scheduled_at"`
	Status       string     `json:"status"`
	SkipReason   *string    `json:"skip_reason"`
	SnapshotID   *int       `json:"snapshot_id"`
	DispatchedAt *time.Time `json:"dispatched_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	InsertedAt   time.Time  `json:"inserted_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
