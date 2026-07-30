package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

// payloadInt coerces a WebSocket payload field to an int.
//
// JSON numbers decode to float64, but the runner has historically sent some IDs
// as strings (snapshot_id did, which is why snapshot status updates silently
// no-op'd). Accepting both means a version-skewed runner degrades gracefully
// instead of failing invisibly.
func payloadInt(payload map[string]any, key string) int {
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// payloadBool coerces a WebSocket payload field to a bool, tolerating the
// stringly-typed forms a runner might send. A missing key is false — which for
// volume_removed is the safe reading: a runner that never confirms the purge
// must not be taken to have performed it.
func payloadBool(payload map[string]any, key string) bool {
	switch v := payload[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// dbActionOutcome maps a completed worker action to the lifecycle status the
// instance should land in.
//
// The runner reports what it *did* ("db_create completed"); the control plane
// decides what the instance *is*. Conflating the two is why the literal string
// "success" used to be written into the status column.
func dbActionOutcome(action string) (structs.DatabaseStatus, string) {
	switch action {
	case socket.MsgDbCreate, socket.MsgDbStart, socket.MsgDbRestart, socket.MsgDbRestore:
		return structs.DBStatusRunning, ""
	case socket.MsgDbStop:
		return structs.DBStatusStopped, ""
	case socket.MsgDbRemove:
		// `remove` destroys the container and keeps the row, so the honest
		// resulting state is "not running". Not `deleting` — that is for a row
		// on its way out — and not `error`, because this was deliberate.
		return structs.DBStatusStopped, ""
	}
	return "", ""
}

// dbActionFailureCode maps a failed worker action to a stable error code.
func dbActionFailureCode(action string) string {
	switch action {
	case socket.MsgDbCreate:
		return structs.DBErrCodeCreateFailed
	case socket.MsgDbStart, socket.MsgDbRestart:
		return structs.DBErrCodeStartFailed
	case socket.MsgDbStop:
		return structs.DBErrCodeStopFailed
	case socket.MsgDbRemove:
		return structs.DBErrCodeRemoveFailed
	case socket.MsgDbRestore:
		return structs.DBErrCodeRestoreFailed
	}
	return structs.DBErrCodeCreateFailed
}

// isAbsentContainerMessage recognises the "there was nothing there" failure a
// pre-idempotency runner reports. Matching on the message is unpleasant, but
// those runners send no code to match on, and the alternative is an instance
// stranded in `error` until someone upgrades the worker.
func isAbsentContainerMessage(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "container not found") ||
		strings.Contains(m, "no such container")
}

// dbInstanceStatus reads an instance's current status, or "" if it cannot be
// loaded.
func dbInstanceStatus(instanceID int) string {
	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		return ""
	}
	return instance.Status
}

// handleDbStatus processes a db_status reply from a worker.
//
// Replies come in two phases: an "ack" confirming the worker picked the command
// up, and a terminal "completed"/"failed" carrying the outcome. Older runners
// send neither phase nor an instance ID — those are logged and dropped rather
// than guessed at, and the reconciler corrects the instance on its next pass.
func handleDbStatus(workerID int, payload map[string]any) {
	instanceID := payloadInt(payload, socket.PayloadDbInstanceID)
	if instanceID == 0 {
		logger.Warn("database", "db_status without a database_instance_id — dropping", logger.F{
			"worker_id": workerID,
			"action":    payload["action"],
			"hint":      "runner predates correlated db replies; the reconciler will correct this instance",
		})
		return
	}

	action, _ := payload["action"].(string)
	phase, _ := payload[socket.PayloadPhase].(string)
	status, _ := payload["status"].(string)
	message, _ := payload["message"].(string)

	// Runners that predate the phase field report bare outcomes.
	if phase == "" {
		switch status {
		case "success", "completed":
			phase = socket.PhaseCompleted
		case "failed", "error":
			phase = socket.PhaseFailed
		default:
			phase = socket.PhaseAck
		}
	}

	switch phase {
	case socket.PhaseAck:
		// The worker has the command. Move out of pending so the watchdog can
		// tell "nobody picked this up" from "this is taking a while".
		if action == socket.MsgDbCreate {
			dbLifecycle.Transition(instanceID, structs.DBStatusProvisioning, transitionOpts{
				Message: "worker accepted create command",
				Actor:   "worker",
			})
			return
		}
		dbLifecycle.RecordEvent(instanceID, structs.DBEventAccepted,
			fmt.Sprintf("worker accepted %s", action), "worker")

	case socket.PhaseCompleted:
		// A delete rides on the same db_remove command as the `remove` action;
		// what separates them is the instance being in `deleting`. When it is,
		// this reply retires the row instead of parking it in `stopped`.
		if action == socket.MsgDbRemove &&
			dbLifecycle.FinalizeDeletion(instanceID, payloadBool(payload, "volume_removed")) {
			return
		}

		next, _ := dbActionOutcome(action)
		if next == "" {
			dbLifecycle.RecordEvent(instanceID, structs.DBEventTransition,
				fmt.Sprintf("%s completed", action), "worker")
			return
		}
		opts := transitionOpts{
			Message: fmt.Sprintf("%s completed", action),
			Actor:   "worker",
		}
		if next == structs.DBStatusRunning {
			now := time.Now().UTC()
			opts.StartedAt = &now
		}
		dbLifecycle.Transition(instanceID, next, opts)

	case socket.PhaseFailed:
		if message == "" {
			message = fmt.Sprintf("%s failed", action)
		}

		// A runner that predates idempotent removal reports an already-absent
		// container as a failure. For the `remove` action that outcome is the
		// goal, so it lands as stopped rather than poisoning the row into error
		// — which is what made a second Remove click leave the instance stuck
		// in `error` with nothing wrong. A delete deliberately does not get
		// this pass: the data volume it was asked to purge is still there.
		if action == socket.MsgDbRemove && isAbsentContainerMessage(message) &&
			dbInstanceStatus(instanceID) != string(structs.DBStatusDeleting) {
			dbLifecycle.Transition(instanceID, structs.DBStatusStopped, transitionOpts{
				Message: "container was already absent; nothing to remove",
				Actor:   "worker",
			})
			return
		}
		dbLifecycle.Transition(instanceID, structs.DBStatusError, transitionOpts{
			Message: message,
			Err:     newDatabaseError(dbActionFailureCode(action), message, true),
			Actor:   "worker",
		})
	}
}

// observedDbContainer is one entry of a worker's db_sync report.
type observedDbContainer struct {
	ContainerName string
	State         string // docker state: running, exited, restarting, created, paused
	Health        string // docker health: healthy, unhealthy, starting, none
	RestartCount  int
	// FatalHint is the worker's diagnosis when it recognises a known-fatal
	// startup failure in the container's logs (bad volume ownership, a
	// non-empty Postgres data directory, and so on).
	FatalHint string
}

// handleDbSync reconciles a worker's full report of the database containers it
// can actually see against what the control plane believes.
//
// This is the level-triggered half of the design: it does not care which
// command produced the current state, only whether observed matches desired. A
// dropped db_status is therefore self-correcting.
func handleDbSync(workerID int, payload map[string]any) {
	rawList, ok := payload["containers"].([]any)
	if !ok {
		return
	}

	observed := map[string]observedDbContainer{}
	for _, raw := range rawList {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["container_name"].(string)
		if name == "" {
			continue
		}
		state, _ := entry["state"].(string)
		health, _ := entry["health"].(string)
		fatalHint, _ := entry["fatal_hint"].(string)
		observed[name] = observedDbContainer{
			ContainerName: name,
			State:         state,
			Health:        health,
			RestartCount:  payloadInt(entry, "restart_count"),
			FatalHint:     fatalHint,
		}
	}

	instances, err := query.ListDatabaseInstancesByWorker(db.DB, workerID)
	if err != nil {
		logger.Error("database", "db_sync failed to list instances", logger.F{
			"worker_id": workerID, "error": err,
		})
		return
	}

	for _, instance := range instances {
		reconcileDatabaseInstance(instance, observed[instance.ContainerName], observed)
	}
}

// reconcileDatabaseInstance drives one instance toward its observed state.
func reconcileDatabaseInstance(instance structs.DatabaseInstance, obs observedDbContainer, observed map[string]observedDbContainer) {
	_, present := observed[instance.ContainerName]
	current := structs.DatabaseStatus(instance.Status)

	if !present {
		// A container that doesn't exist has no health to report.
		dbLifecycle.SetHealth(instance.ID, structs.DBHealthNone, "container is not present on the worker")

		// Never provisioned, or provisioning is still in flight — the watchdog
		// owns those, not the reconciler.
		if current.IsTransitional() {
			return
		}
		// Already reported; don't rewrite the error on every tick.
		if current == structs.DBStatusError {
			return
		}
		// A stopped instance with no container is the expected outcome of the
		// `remove` action, which destroys the container but keeps the record.
		// An out-of-band `docker rm` is indistinguishable from that, and
		// flagging every deliberately removed database as failed forever is
		// worse than leaving it stopped — which is accurate either way.
		if current == structs.DBStatusStopped {
			return
		}
		dbLifecycle.Transition(instance.ID, structs.DBStatusError, transitionOpts{
			Message: "container is no longer present on the worker",
			Err: newDatabaseError(structs.DBErrCodeContainerMissing,
				fmt.Sprintf("container %s was not found on worker %d", instance.ContainerName, instance.WorkerID), true),
			Actor: "reconciler",
		})
		return
	}

	// A container flapping is neither healthy nor cleanly failed — surface it
	// as degraded so it is visibly wrong instead of oscillating. When the worker
	// recognised the specific cause, report that rather than the restart count.
	if obs.RestartCount >= 3 && (obs.State == "restarting" || obs.FatalHint != "") {
		code := structs.DBErrCodeRestartLoop
		detail := fmt.Sprintf("container %s has restarted %d times; check its logs for an initialisation error",
			instance.ContainerName, obs.RestartCount)
		if obs.FatalHint != "" {
			code = structs.DBErrCodeInitFailed
			detail = obs.FatalHint
		}
		dbLifecycle.Transition(instance.ID, structs.DBStatusDegraded, transitionOpts{
			Message: fmt.Sprintf("container is restarting repeatedly (%d restarts)", obs.RestartCount),
			Err:     newDatabaseError(code, detail, false),
			Actor:   "reconciler",
		})
		return
	}

	switch obs.State {
	case "running":
		if current != structs.DBStatusRunning {
			opts := transitionOpts{
				Message: "container observed running on the worker",
				Actor:   "reconciler",
			}
			if instance.StartedAt == nil {
				now := time.Now().UTC()
				opts.StartedAt = &now
			}
			dbLifecycle.Transition(instance.ID, structs.DBStatusRunning, opts)
		}
	case "exited", "created", "dead":
		// Don't fight a delete that is still in flight.
		if current == structs.DBStatusDeleting {
			return
		}
		if current != structs.DBStatusStopped && current != structs.DBStatusError {
			dbLifecycle.Transition(instance.ID, structs.DBStatusStopped, transitionOpts{
				Message: fmt.Sprintf("container observed %s on the worker", obs.State),
				Actor:   "reconciler",
			})
		}
	}

	// Health is only meaningful while the container is actually up. Docker keeps
	// reporting the last health state of an exited container, so trusting it
	// verbatim leaves a stopped database sitting at "healthy" indefinitely —
	// the same staleness the container sync path guards against.
	if obs.State != "running" && obs.State != "paused" {
		dbLifecycle.SetHealth(instance.ID, structs.DBHealthNone, "container is not running")
		return
	}

	if obs.Health != "" {
		health := structs.DatabaseHealth(obs.Health)
		if health.IsValid() {
			dbLifecycle.SetHealth(instance.ID, health, "observed by worker sync")
		}
	}
}
