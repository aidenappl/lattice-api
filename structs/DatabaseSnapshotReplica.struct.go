package structs

import "time"

// Replica roles and statuses.
const (
	ReplicaRolePrimary = "primary"
	ReplicaRoleMirror  = "mirror"

	ReplicaPending   = "pending"
	ReplicaCompleted = "completed"
	ReplicaFailed    = "failed"
)

// DatabaseSnapshotReplica is one copy of a snapshot on one destination.
//
// The primary and the mirror succeed or fail independently. A mirror failure
// degrades backup posture but must never fail the snapshot: the primary copy
// exists, and reporting the backup as failed would push an operator toward
// re-running a dump that already succeeded.
type DatabaseSnapshotReplica struct {
	ID                  int        `json:"id"`
	SnapshotID          int        `json:"snapshot_id"`
	BackupDestinationID int        `json:"backup_destination_id"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	SizeBytes           *int64     `json:"size_bytes"`
	ErrorMessage        *string    `json:"error_message"`
	CompletedAt         *time.Time `json:"completed_at"`
	InsertedAt          time.Time  `json:"inserted_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}
