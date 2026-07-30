package main

import (
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

// databaseLifecycle owns every write to database_instances.status,
// .health_status and .last_error.
//
// Routing all of them through one place is deliberate: the subsystem's original
// failure mode was a status write that silently no-op'd, leaving instances in
// "pending" with nothing recorded anywhere. Every transition here writes an
// event and broadcasts to admin clients, so a state change can never happen
// invisibly.
type databaseLifecycle struct {
	adminHub *socket.AdminHub
}

// dbLifecycle is set during initApp. Nil-safe: every method tolerates a nil
// receiver so unit tests and the migrate-encrypt path don't need to construct it.
var dbLifecycle *databaseLifecycle

// transitionOpts carries the optional detail attached to a status change.
type transitionOpts struct {
	// Message is the human-readable reason, recorded on the event.
	Message string
	// Err, when set, is written to last_error. Any transition to error should
	// set it. Transitions to a healthy state clear it automatically.
	Err *structs.DatabaseError
	// Actor identifies who caused this ("reconciler", "watchdog", a username).
	Actor string
	// StartedAt stamps the instance's start time (set when entering running).
	StartedAt *time.Time
}

// Transition moves an instance to a new status, recording an event and
// broadcasting the change. It is idempotent: transitioning to the status the
// instance already holds writes nothing and emits no event, so the reconciler
// can call it every tick without flooding the audit trail.
func (l *databaseLifecycle) Transition(instanceID int, to structs.DatabaseStatus, opts transitionOpts) {
	if !to.IsValid() {
		logger.Error("database", "refusing to write invalid status", logger.F{
			"database_instance_id": instanceID, "status": string(to),
		})
		return
	}

	current, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		logger.Error("database", "transition failed to load instance", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		return
	}

	statusChanged := current.Status != string(to)

	// Recovering into a healthy state must clear a stale error, otherwise the
	// UI shows a running instance still carrying its last failure.
	clearErr := opts.Err == nil && current.LastError != nil &&
		(to == structs.DBStatusRunning || to == structs.DBStatusStopped)

	errChanged := opts.Err != nil &&
		(current.LastError == nil ||
			current.LastError.Code != opts.Err.Code ||
			current.LastError.Message != opts.Err.Message)

	if !statusChanged && !clearErr && !errChanged && opts.StartedAt == nil {
		return
	}

	statusStr := string(to)
	req := query.UpdateDatabaseInstanceRequest{
		Status:         &statusStr,
		LastError:      opts.Err,
		ClearLastError: clearErr,
		StartedAt:      opts.StartedAt,
	}

	if _, err := query.UpdateDatabaseInstance(db.DB, instanceID, req); err != nil {
		logger.Error("database", "transition failed to write status", logger.F{
			"database_instance_id": instanceID, "status": statusStr, "error": err,
		})
		return
	}

	message := opts.Message
	if message == "" {
		message = fmt.Sprintf("status changed from %s to %s", current.Status, statusStr)
	}

	kind := structs.DBEventTransition
	var code *string
	if opts.Err != nil {
		kind = structs.DBEventFailed
		c := opts.Err.Code
		code = &c
	}

	// Health describes a running container. Once an instance is stopped,
	// deleted or failed, any health we hold is stale — Docker keeps reporting
	// the last health state of an exited container, so leaving it would show a
	// stopped database as "healthy" until something else corrected it.
	// `degraded` is excluded: the container is still there and its health is
	// exactly what makes it interesting.
	if to != structs.DBStatusRunning && to != structs.DBStatusDegraded &&
		current.HealthStatus != string(structs.DBHealthNone) {
		l.SetHealth(instanceID, structs.DBHealthNone, "container is no longer running")
	}

	l.recordEvent(instanceID, kind, &statusStr, message, code, opts.Actor)
	l.broadcast("db_instance_changed", map[string]any{
		"database_instance_id": instanceID,
		"status":               statusStr,
		"message":              message,
		"last_error":           opts.Err,
	})

	logger.Info("database", "instance status changed", logger.F{
		"database_instance_id": instanceID,
		"name":                 current.Name,
		"from":                 current.Status,
		"to":                   statusStr,
		"actor":                opts.Actor,
	})
}

// BeginDeleting marks an instance as being torn down. The row stays active
// until the worker confirms the container and volume are gone — see
// FinalizeDeletion — so a failed teardown is visible and retryable instead of
// vanishing from the UI with its resources still on disk.
func (l *databaseLifecycle) BeginDeleting(instanceID int, actor string) {
	if actor == "" {
		actor = "api"
	}
	l.Transition(instanceID, structs.DBStatusDeleting, transitionOpts{
		Message: "destroying container and data volume",
		Actor:   actor,
	})
}

// FinalizeDeletion retires the row of an instance whose worker has confirmed
// the container and data volume are gone.
//
// Returns false when the instance was not being deleted, which is how a plain
// `remove` action — same command, container only — is told apart from a delete.
func (l *databaseLifecycle) FinalizeDeletion(instanceID int, volumeRemoved bool) bool {
	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		logger.Error("database", "delete finalisation failed to load instance", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		return false
	}
	if instance.Status != string(structs.DBStatusDeleting) {
		return false
	}

	// A runner that predates volume purging removes the container and reports
	// success without the confirmation. Retiring the row on that reply would
	// leak the data volume with nothing left pointing at it, so the delete is
	// held open as a retryable error instead.
	if !volumeRemoved {
		message := fmt.Sprintf("worker did not confirm removal of data volume %s; upgrade the runner on worker %d and retry the delete",
			instance.VolumeName, instance.WorkerID)
		l.Transition(instanceID, structs.DBStatusError, transitionOpts{
			Message: message,
			Err:     newDatabaseError(structs.DBErrCodeRemoveFailed, message, true),
			Actor:   "worker",
		})
		return true
	}

	if err := query.DeleteDatabaseInstance(db.DB, instanceID); err != nil {
		message := fmt.Sprintf("container and data volume were destroyed but the record could not be retired: %v", err)
		logger.Error("database", "delete finalisation failed to retire row", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		l.Transition(instanceID, structs.DBStatusError, transitionOpts{
			Message: message,
			Err:     newDatabaseError(structs.DBErrCodeRemoveFailed, message, true),
			Actor:   "worker",
		})
		return true
	}

	l.recordEvent(instanceID, structs.DBEventTransition, nil,
		fmt.Sprintf("deleted: container %s and data volume %s destroyed, record retired",
			instance.ContainerName, instance.VolumeName), nil, "worker")
	l.broadcast("db_instance_deleted", map[string]any{
		"database_instance_id": instanceID,
		"name":                 instance.Name,
	})
	logger.Info("database", "instance deleted", logger.F{
		"database_instance_id": instanceID,
		"name":                 instance.Name,
		"worker_id":            instance.WorkerID,
		"volume":               instance.VolumeName,
	})
	return true
}

// SetHealth records observed container health, independently of status. A
// running instance can legitimately be unhealthy.
func (l *databaseLifecycle) SetHealth(instanceID int, health structs.DatabaseHealth, message string) {
	if !health.IsValid() {
		logger.Error("database", "refusing to write invalid health status", logger.F{
			"database_instance_id": instanceID, "health_status": string(health),
		})
		return
	}

	current, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		return
	}
	if current.HealthStatus == string(health) {
		return
	}

	healthStr := string(health)
	if _, err := query.UpdateDatabaseInstance(db.DB, instanceID, query.UpdateDatabaseInstanceRequest{
		HealthStatus: &healthStr,
	}); err != nil {
		logger.Error("database", "failed to write health status", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		return
	}

	if message == "" {
		message = fmt.Sprintf("health changed from %s to %s", current.HealthStatus, healthStr)
	}
	l.recordEvent(instanceID, structs.DBEventHealth, nil, message, nil, "worker")
	l.broadcast("db_health_status", map[string]any{
		"database_instance_id": instanceID,
		"health_status":        healthStr,
	})
}

// RecordEvent appends an entry to an instance's history without changing state.
// Used for requests, acknowledgements, console opens and credential reveals.
func (l *databaseLifecycle) RecordEvent(instanceID int, kind, message, actor string) {
	l.recordEvent(instanceID, kind, nil, message, nil, actor)
}

func (l *databaseLifecycle) recordEvent(instanceID int, kind string, status *string, message string, code *string, actor string) {
	var actorPtr *string
	if actor != "" {
		actorPtr = &actor
	}
	if err := query.CreateDatabaseInstanceEvent(db.DB, query.CreateDatabaseInstanceEventRequest{
		DatabaseInstanceID: instanceID,
		Kind:               kind,
		Status:             status,
		Message:            message,
		Code:               code,
		Actor:              actorPtr,
	}); err != nil {
		logger.Error("database", "failed to record instance event", logger.F{
			"database_instance_id": instanceID, "kind": kind, "error": err,
		})
	}
}

func (l *databaseLifecycle) broadcast(msgType string, payload map[string]any) {
	if l == nil || l.adminHub == nil {
		return
	}
	l.adminHub.BroadcastJSON(map[string]any{
		"type":    msgType,
		"payload": payload,
	})
}

// newDatabaseError builds a structured failure record stamped with the current
// time.
func newDatabaseError(code, message string, retryable bool) *structs.DatabaseError {
	return &structs.DatabaseError{
		Code:       code,
		Message:    message,
		OccurredAt: time.Now().UTC(),
		Retryable:  retryable,
	}
}
