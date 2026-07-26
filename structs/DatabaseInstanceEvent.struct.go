package structs

import "time"

// Event kinds recorded against a database instance. The event log is the
// audit trail that answers "why is this instance in the state it's in" —
// every status transition and every failure writes one.
const (
	DBEventRequested   = "requested"    // an operator asked for something
	DBEventAccepted    = "accepted"     // the worker acknowledged the command
	DBEventTransition  = "transition"   // status changed
	DBEventHealth      = "health"       // health_status changed
	DBEventFailed      = "failed"       // an operation failed
	DBEventReconciled  = "reconciled"   // the reconciler corrected drift
	DBEventConsoleOpen = "console_open" // an interactive console was opened
	DBEventReveal      = "reveal"       // credentials were revealed
)

// DatabaseInstanceEvent is one append-only entry in an instance's history.
type DatabaseInstanceEvent struct {
	ID                 int       `json:"id"`
	DatabaseInstanceID int       `json:"database_instance_id"`
	Kind               string    `json:"kind"`
	Status             *string   `json:"status"`
	Message            string    `json:"message"`
	Code               *string   `json:"code"`
	Actor              *string   `json:"actor"`
	RecordedAt         time.Time `json:"recorded_at"`
}
