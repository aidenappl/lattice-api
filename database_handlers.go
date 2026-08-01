package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/routers"
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

// ensureScheduledSnapshotRow returns the row for a scheduled snapshot,
// creating it on first sight.
//
// Scheduled runs are triggered by the runner's cron, so unlike a manual snapshot
// there is no row waiting for them. Adopting on the first status message is what
// makes a scheduled snapshot visible at all: before this, the runner sent a
// synthetic string id, the orchestrator parsed it to 0, dropped the message, and
// no scheduled snapshot was ever recorded — successful or otherwise.
func ensureScheduledSnapshotRow(instanceID int, filename string) (*structs.DatabaseSnapshot, error) {
	existing, err := query.GetSnapshotByInstanceAndFilename(db.DB, instanceID, filename)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, query.ErrNotFound) {
		return nil, err
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		return nil, err
	}

	return query.CreateSnapshot(db.DB, query.CreateSnapshotRequest{
		DatabaseInstanceID:  instance.ID,
		BackupDestinationID: instance.BackupDestinationID,
		Filename:            filename,
		Engine:              instance.Engine,
		DatabaseName:        instance.DatabaseName,
		TriggerType:         "scheduled",
	})
}

// finaliseDeleteAfterSnapshot destroys a database whose final snapshot has just
// completed.
//
// This is the second half of a delete that asked for a last backup. The ordering
// is the whole point: the volume may only be destroyed once a restorable copy
// exists, so the delete waits here rather than racing the dump. If the snapshot
// had failed, this is never reached and the database is still present — the
// correct outcome, since the operator asked not to lose it without a copy.
//
// Returns true when it took ownership of the instance.
func finaliseDeleteAfterSnapshot(instanceID int, hub *socket.WorkerHub) bool {
	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil || !instance.PendingFinalSnapshot {
		return false
	}

	pending := false
	if _, err := query.UpdateDatabaseInstance(db.DB, instanceID, query.UpdateDatabaseInstanceRequest{
		PendingFinalSnapshot: &pending,
	}); err != nil {
		logger.Error("database", "failed to clear pending final snapshot", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		return false
	}

	if !hub.IsConnected(instance.WorkerID) {
		message := fmt.Sprintf("final snapshot completed but worker %d is no longer connected; the database was not destroyed", instance.WorkerID)
		dbLifecycle.SetWarning(instanceID, newDatabaseError(structs.DBErrCodeRemoveFailed, message, true), "control-plane")
		return true
	}

	payload := map[string]any{
		socket.PayloadDbInstanceID:  instanceID,
		socket.PayloadContainerName: instance.ContainerName,
		socket.PayloadVolumeName:    instance.VolumeName,
		socket.PayloadRemoveVolume:  true,
	}
	if err := hub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbRemove,
		Payload: payload,
	}); err != nil {
		logger.Error("database", "failed to send delete after final snapshot", logger.F{
			"database_instance_id": instanceID, "error": err,
		})
		return true
	}

	dbLifecycle.BeginDeleting(instanceID, "control-plane")
	logger.Info("database", "final snapshot complete, destroying database", logger.F{
		"database_instance_id": instanceID,
		"name":                 instance.Name,
	})
	return true
}

// applySnapshotRetention prunes an instance's oldest successful snapshots once a
// new one completes, removing the remote file as well as the row.
//
// Retention was previously plumbed end to end and enforced nowhere:
// retention_count was accepted on create and update, forwarded to the runner,
// stored in the scheduler job — and never read, so snapshots accrued forever on
// whatever destination they were written to.
//
// Two deliberate properties:
//   - A retention failure never fails the snapshot that triggered it. Losing an
//     old copy is a storage problem; failing the new backup is a data problem.
//   - A redundancy floor: retention never reduces an instance below
//     minSnapshotRedundancy successful copies, whatever retention_count says.
//     "Keep 1" plus a silently-failing backup is how a single corrupt artifact
//     becomes the only artifact.
func applySnapshotRetention(instanceID int, hub *socket.WorkerHub) {
	const minSnapshotRedundancy = 2

	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil || instance.RetentionCount == nil || *instance.RetentionCount <= 0 {
		return
	}

	keep := *instance.RetentionCount
	if keep < minSnapshotRedundancy {
		logger.Info("database", "retention floor raised the configured count", logger.F{
			"database_instance_id": instanceID,
			"configured":           keep,
			"effective":            minSnapshotRedundancy,
			"reason":               "never reduce an instance below two successful snapshots",
		})
		keep = minSnapshotRedundancy
	}

	snapshots, err := query.ListSnapshotsByInstance(db.DB, instanceID)
	if err != nil || snapshots == nil {
		return
	}

	// Newest first, successful only — a failed or in-flight row is not a copy.
	var successful []structs.DatabaseSnapshot
	for _, s := range *snapshots {
		if s.Status == "completed" {
			successful = append(successful, s)
		}
	}
	sort.Slice(successful, func(i, j int) bool { return successful[i].ID > successful[j].ID })

	if len(successful) <= keep {
		return
	}

	for _, stale := range successful[keep:] {
		if err := routers.DeleteSnapshotArtifact(hub, &stale, instance); err != nil {
			logger.Warn("database", "retention could not remove an old snapshot", logger.F{
				"database_instance_id": instanceID, "snapshot_id": stale.ID, "error": err,
			})
			continue
		}
		logger.Info("database", "retention removed an old snapshot", logger.F{
			"database_instance_id": instanceID,
			"snapshot_id":          stale.ID,
			"filename":             stale.Filename,
			"kept":                 keep,
		})
	}
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
	// VolumeSizeBytes is the data volume's measured size, or 0 when the worker
	// has not managed to measure it. Reported on a slow cadence: the measurement
	// walks the volume, so it is cached worker-side between syncs.
	VolumeSizeBytes int64
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
			ContainerName:   name,
			State:           state,
			Health:          health,
			RestartCount:    payloadInt(entry, "restart_count"),
			FatalHint:       fatalHint,
			VolumeSizeBytes: int64(payloadInt(entry, "volume_size_bytes")),
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

	// Record what the database costs on disk whenever the worker reports it.
	// Independent of status: a stopped database still occupies its volume, and
	// that is exactly when someone is deciding whether to delete it.
	if obs.VolumeSizeBytes > 0 &&
		(instance.VolumeSizeBytes == nil || *instance.VolumeSizeBytes != obs.VolumeSizeBytes) {
		size := obs.VolumeSizeBytes
		if _, err := query.UpdateDatabaseInstance(db.DB, instance.ID, query.UpdateDatabaseInstanceRequest{
			VolumeSizeBytes: &size,
		}); err != nil {
			logger.Warn("database", "failed to record volume size", logger.F{
				"database_instance_id": instance.ID, "error": err,
			})
		}
	}

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

// recordPrimaryReplicaAndMirror records the primary copy of a completed snapshot
// and, if the instance has a mirror destination, asks the worker to copy it
// there.
//
// The mirror is a separate step *after* the primary succeeds, not a second
// concurrent upload. Streaming to both at once would let the slower destination
// apply backpressure to the dump — the same hazard that makes an unbuffered pipe
// dangerous — and a partial failure would leave an artifact whose state is
// ambiguous. Sequential with per-replica status keeps the primary's success
// independent of the mirror's.
func recordPrimaryReplicaAndMirror(snapshotID int, sizeBytes *int64, hub *socket.WorkerHub) {
	snapshot, err := query.GetSnapshotByID(db.DB, snapshotID)
	if err != nil || snapshot.BackupDestinationID == nil {
		return
	}

	if err := query.UpsertSnapshotReplica(db.DB, query.UpsertReplicaRequest{
		SnapshotID:          snapshot.ID,
		BackupDestinationID: *snapshot.BackupDestinationID,
		Role:                structs.ReplicaRolePrimary,
		Status:              structs.ReplicaCompleted,
		SizeBytes:           sizeBytes,
	}); err != nil {
		logger.Error("database", "failed to record primary replica", logger.F{
			"snapshot_id": snapshot.ID, "error": err,
		})
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, snapshot.DatabaseInstanceID)
	if err != nil || instance.MirrorBackupDestinationID == nil {
		return
	}
	if *instance.MirrorBackupDestinationID == *snapshot.BackupDestinationID {
		return // a mirror to the same destination is not a second copy
	}

	source, err := query.GetBackupDestinationByID(db.DB, *snapshot.BackupDestinationID)
	if err != nil {
		return
	}
	target, err := query.GetBackupDestinationByID(db.DB, *instance.MirrorBackupDestinationID)
	if err != nil {
		return
	}

	_ = query.UpsertSnapshotReplica(db.DB, query.UpsertReplicaRequest{
		SnapshotID:          snapshot.ID,
		BackupDestinationID: target.ID,
		Role:                structs.ReplicaRoleMirror,
		Status:              structs.ReplicaPending,
	})

	if !hub.IsConnected(instance.WorkerID) {
		reason := fmt.Sprintf("worker %d is not connected", instance.WorkerID)
		_ = query.UpsertSnapshotReplica(db.DB, query.UpsertReplicaRequest{
			SnapshotID: snapshot.ID, BackupDestinationID: target.ID,
			Role: structs.ReplicaRoleMirror, Status: structs.ReplicaFailed, ErrorMessage: &reason,
		})
		return
	}

	payload := map[string]any{
		socket.PayloadDbInstanceID:      instance.ID,
		socket.PayloadSnapshotID:        snapshot.ID,
		socket.PayloadFilename:          snapshot.Filename,
		socket.PayloadSourceDestination: destinationPayload(source),
		socket.PayloadTargetDestination: destinationPayload(target),
	}

	if err := hub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbMirrorSnapshot,
		Payload: payload,
	}); err != nil {
		reason := err.Error()
		_ = query.UpsertSnapshotReplica(db.DB, query.UpsertReplicaRequest{
			SnapshotID: snapshot.ID, BackupDestinationID: target.ID,
			Role: structs.ReplicaRoleMirror, Status: structs.ReplicaFailed, ErrorMessage: &reason,
		})
	}
}

func destinationPayload(dest *structs.BackupDestination) map[string]any {
	out := map[string]any{socket.PayloadDestType: dest.Type}
	if dest.Config != nil {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(*dest.Config), &cfg); err == nil {
			out[socket.PayloadDestConfig] = cfg
		}
	}
	return out
}

// handleMirrorStatus records the outcome of a mirror copy.
//
// A failed mirror never fails the snapshot: the primary copy exists, and marking
// the backup failed would push an operator toward re-running a dump that already
// succeeded. It degrades backup posture instead, which is exactly the signal
// that should move.
func handleMirrorStatus(payload map[string]any) {
	snapshotID := payloadInt(payload, socket.PayloadSnapshotID)
	status, _ := payload[socket.PayloadStatus].(string)
	if snapshotID == 0 || status == "" {
		return
	}

	snapshot, err := query.GetSnapshotByID(db.DB, snapshotID)
	if err != nil {
		return
	}
	instance, err := query.GetDatabaseInstanceByID(db.DB, snapshot.DatabaseInstanceID)
	if err != nil || instance.MirrorBackupDestinationID == nil {
		return
	}

	req := query.UpsertReplicaRequest{
		SnapshotID:          snapshotID,
		BackupDestinationID: *instance.MirrorBackupDestinationID,
		Role:                structs.ReplicaRoleMirror,
		Status:              structs.ReplicaFailed,
	}
	if status == "completed" {
		req.Status = structs.ReplicaCompleted
		if sb, ok := payload[socket.PayloadSizeBytes].(float64); ok && sb > 0 {
			v := int64(sb)
			req.SizeBytes = &v
		}
	} else if em, ok := payload[socket.PayloadErrorMessage].(string); ok && em != "" {
		req.ErrorMessage = &em
	}

	if err := query.UpsertSnapshotReplica(db.DB, req); err != nil {
		logger.Error("database", "failed to record mirror replica", logger.F{
			"snapshot_id": snapshotID, "error": err,
		})
		return
	}

	logger.Info("database", "snapshot mirror "+req.Status, logger.F{
		"snapshot_id":          snapshotID,
		"database_instance_id": instance.ID,
	})
}
