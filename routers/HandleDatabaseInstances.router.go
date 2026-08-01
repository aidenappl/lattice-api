package routers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type DatabaseHandler struct {
	WorkerHub *socket.WorkerHub
	AdminHub  *socket.AdminHub
	// Lifecycle is the control plane's single owner of status writes. Handlers
	// go through it rather than writing database_instances.status directly, so
	// no state change can happen without an event and a broadcast. Optional:
	// nil-safe for tests that exercise a handler without the full app.
	Lifecycle DatabaseLifecycle
}

// DatabaseLifecycle is the slice of the lifecycle owner that HTTP handlers
// need. Kept as an interface here because the implementation lives in package
// main alongside the reconciler that shares it.
type DatabaseLifecycle interface {
	// BeginDeleting marks an instance as being torn down, so a concurrent
	// reconcile does not read the still-present container as drift and the UI
	// shows the teardown while it is in flight.
	BeginDeleting(instanceID int, actor string)
}

// dbActor names whoever made a request, for the lifecycle event trail.
func dbActor(r *http.Request) string {
	if r == nil {
		return ""
	}
	if user, _ := middleware.GetUserFromContext(r.Context()); user != nil {
		return user.Email
	}
	return ""
}

// dbEvent appends an entry to an instance's history. Failures are logged, never
// surfaced — losing an audit line must not fail the operation that produced it.
func dbEvent(instanceID int, kind, message string, r *http.Request) {
	var actor *string
	if r != nil {
		if user, _ := middleware.GetUserFromContext(r.Context()); user != nil {
			name := user.Email
			actor = &name
		}
	}
	if err := query.CreateDatabaseInstanceEvent(db.DB, query.CreateDatabaseInstanceEventRequest{
		DatabaseInstanceID: instanceID,
		Kind:               kind,
		Message:            message,
		Actor:              actor,
	}); err != nil {
		log.Printf("database instance %d: failed to record %s event: %v", instanceID, kind, err)
	}
}

// dbCommandPayload builds the correlated envelope payload shared by every
// database command. Every command carries the instance ID, a per-attempt
// request ID and a stable idempotency key, and every worker reply echoes them
// back — without which a reply cannot be matched to the row it should update.
func dbCommandPayload(instanceID int, action string) map[string]any {
	return map[string]any{
		socket.PayloadDbInstanceID:   instanceID,
		socket.PayloadRequestID:      uuid.NewString(),
		socket.PayloadIdempotencyKey: fmt.Sprintf("%s:%d", action, instanceID),
	}
}

// HandleListDatabaseInstances returns all active database instances with optional filters.
func HandleListDatabaseInstances(w http.ResponseWriter, r *http.Request) {
	var req query.ListDatabaseInstancesRequest

	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerID = &n
		}
	}
	if v := r.URL.Query().Get("engine"); v != "" {
		req.Engine = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		req.Status = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Offset = n
		}
	}

	instances, total, err := query.ListDatabaseInstances(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list database instances")
		return
	}

	responder.NewWithCount(w, instances, total, "", "", "database instances retrieved")
}

// HandleGetDatabaseInstance returns a single database instance by ID.
func HandleGetDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	responder.New(w, instance, "database instance retrieved")
}

// HandleListDatabaseInstanceEvents returns an instance's lifecycle history —
// the audit trail that explains how it reached its current state.
func HandleListDatabaseInstanceEvents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	req := query.ListDatabaseInstanceEventsRequest{DatabaseInstanceID: id}
	if v := r.URL.Query().Get("kind"); v != "" {
		req.Kind = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			req.Offset = n
		}
	}

	events, total, err := query.ListDatabaseInstanceEvents(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list database instance events")
		return
	}

	responder.NewWithCount(w, events, total, "", "", "database instance events retrieved")
}

// HandleGetWorkerPortAvailability reports which host ports are already claimed
// on a worker and suggests a free one, so the UI can validate before submitting
// rather than surfacing a collision as an opaque provisioning failure.
func HandleGetWorkerPortAvailability(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	claimed, err := query.ClaimedPortsOnWorker(db.DB, workerID)
	if err != nil {
		responder.QueryError(w, err, "failed to read claimed ports")
		return
	}

	// Callers can ask about one specific port, which is what the create form does.
	if v := r.URL.Query().Get("port"); v != "" {
		port, convErr := strconv.Atoi(v)
		if convErr != nil {
			responder.SendError(w, http.StatusBadRequest, "invalid port")
			return
		}
		conflict, exists := claimed[port]
		payload := map[string]any{"port": port, "available": !exists}
		if exists {
			payload["conflict"] = conflict
		}
		responder.New(w, payload, "port availability retrieved")
		return
	}

	claimedList := make([]query.PortConflict, 0, len(claimed))
	for _, c := range claimed {
		claimedList = append(claimedList, c)
	}

	var suggested *int
	if port, allocErr := query.AllocateDatabasePort(db.DB, workerID); allocErr == nil {
		suggested = &port
	}

	responder.New(w, map[string]any{
		"worker_id":      workerID,
		"claimed":        claimedList,
		"suggested_port": suggested,
		"range_min":      query.DB_PORT_RANGE_MIN,
		"range_max":      query.DB_PORT_RANGE_MAX,
	}, "port availability retrieved")
}

// HandleCreateDatabaseInstance creates a new database instance and sends
// the db_create command to the target worker.
func (h *DatabaseHandler) HandleCreateDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name                string   `json:"name"`
		Engine              string   `json:"engine"`
		EngineVersion       string   `json:"engine_version"`
		WorkerID            int      `json:"worker_id"`
		Port                int      `json:"port"`
		RootPassword        string   `json:"root_password"`
		DatabaseName        string   `json:"database_name"`
		Username            string   `json:"username"`
		Password            string   `json:"password"`
		CPULimit            *float64 `json:"cpu_limit"`
		MemoryLimit         *int     `json:"memory_limit"`
		SnapshotSchedule    *string  `json:"snapshot_schedule"`
		RetentionCount      *int     `json:"retention_count"`
		BackupDestinationID *int     `json:"backup_destination_id"`
		// AdoptExistingVolume reuses a leftover data volume of the same name.
		// Off by default because the engine then skips initialisation and keeps
		// its previous credentials, silently ignoring the ones generated here.
		AdoptExistingVolume bool `json:"adopt_existing_volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Validate required fields
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if err := tools.ValidateName(body.Name); err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}
	existing, _ := query.GetDatabaseInstanceByName(db.DB, body.Name)
	if existing != nil {
		responder.SendError(w, http.StatusConflict, "a database instance with that name already exists")
		return
	}
	if body.Engine == "" {
		responder.MissingBodyFields(w, "engine")
		return
	}
	if body.DatabaseName == "" {
		responder.MissingBodyFields(w, "database_name")
		return
	}
	if body.Username == "" {
		responder.MissingBodyFields(w, "username")
		return
	}
	if body.WorkerID == 0 {
		responder.MissingBodyFields(w, "worker_id")
		return
	}

	// Validate engine
	if body.Engine != "mysql" && body.Engine != "mariadb" && body.Engine != "postgres" {
		responder.SendError(w, http.StatusBadRequest, "engine must be one of: mysql, mariadb, postgres")
		return
	}

	if msg := validateSnapshotSchedulable(body.SnapshotSchedule, body.BackupDestinationID); msg != "" {
		responder.SendError(w, http.StatusBadRequest, msg)
		return
	}

	// Default engine version
	if body.EngineVersion == "" {
		switch body.Engine {
		case "mysql":
			body.EngineVersion = "8"
		case "mariadb":
			body.EngineVersion = "11"
		case "postgres":
			body.EngineVersion = "16"
		}
	}

	// Port: allocate one from the managed range when unspecified, otherwise
	// honour the caller's choice after checking it is actually free. Either way
	// a collision is reported here as a 409 rather than discovered later as an
	// opaque Docker bind failure on the worker.
	if body.Port == 0 {
		port, err := query.AllocateDatabasePort(db.DB, body.WorkerID)
		if err != nil {
			if errors.Is(err, query.ErrNoFreePort) {
				responder.SendError(w, http.StatusConflict,
					fmt.Sprintf("no free host port available on worker %d in range %d-%d",
						body.WorkerID, query.DB_PORT_RANGE_MIN, query.DB_PORT_RANGE_MAX))
				return
			}
			responder.QueryError(w, err, "failed to allocate a host port")
			return
		}
		body.Port = port
	} else {
		conflict, err := query.FindPortConflict(db.DB, body.WorkerID, body.Port, 0)
		if err != nil {
			responder.QueryError(w, err, "failed to check port availability")
			return
		}
		if conflict != nil {
			responder.SendError(w, http.StatusConflict,
				fmt.Sprintf("port %d is already in use on worker %d by %s %q",
					body.Port, body.WorkerID, conflict.Kind, conflict.Name))
			return
		}
	}

	containerName := "lattice-db-" + body.Name
	volumeName := "lattice-dbdata-" + body.Name

	// Generate random passwords if not provided
	if body.RootPassword == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to generate root password")
			return
		}
		body.RootPassword = hex.EncodeToString(b)
	}
	if body.Password == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to generate password")
			return
		}
		body.Password = hex.EncodeToString(b)
	}

	// Check worker is connected
	if !h.WorkerHub.IsConnected(body.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	instance, err := query.CreateDatabaseInstance(db.DB, query.CreateDatabaseInstanceRequest{
		Name:                body.Name,
		Engine:              body.Engine,
		EngineVersion:       body.EngineVersion,
		WorkerID:            body.WorkerID,
		Port:                body.Port,
		RootPassword:        body.RootPassword,
		DatabaseName:        body.DatabaseName,
		Username:            body.Username,
		Password:            body.Password,
		CPULimit:            body.CPULimit,
		MemoryLimit:         body.MemoryLimit,
		SnapshotSchedule:    body.SnapshotSchedule,
		RetentionCount:      body.RetentionCount,
		BackupDestinationID: body.BackupDestinationID,
		ContainerName:       containerName,
		VolumeName:          volumeName,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create database instance")
		return
	}

	dbEvent(instance.ID, structs.DBEventRequested,
		fmt.Sprintf("create requested on worker %d, port %d", body.WorkerID, body.Port), r)

	// Send db_create to worker
	payload := dbCommandPayload(instance.ID, socket.MsgDbCreate)
	payload["container_name"] = containerName
	payload["volume_name"] = volumeName
	payload["engine"] = body.Engine
	payload["engine_version"] = body.EngineVersion
	payload["port"] = body.Port
	payload["root_password"] = body.RootPassword
	payload["database_name"] = body.DatabaseName
	payload["username"] = body.Username
	payload["password"] = body.Password
	payload["adopt_existing_volume"] = body.AdoptExistingVolume

	if body.CPULimit != nil {
		payload["cpu_limit"] = *body.CPULimit
	}
	if body.MemoryLimit != nil {
		// The API and UI speak megabytes; Docker's resource limit is bytes.
		// Sending the raw value made every create fail — Docker rejects any
		// limit below 6MB, so a 512 "MB" request became 512 bytes.
		payload["memory_limit"] = int64(*body.MemoryLimit) * 1024 * 1024
	}

	if err := h.WorkerHub.SendJSONToWorker(body.WorkerID, socket.Envelope{
		Type:    socket.MsgDbCreate,
		Payload: payload,
	}); err != nil {
		// The row exists but the worker never heard about it. Record that
		// plainly instead of leaving it to sit in pending forever.
		msg := fmt.Sprintf("failed to send create command to worker: %v", err)
		failed := string(structs.DBStatusError)
		_, _ = query.UpdateDatabaseInstance(db.DB, instance.ID, query.UpdateDatabaseInstanceRequest{
			Status: &failed,
			LastError: &structs.DatabaseError{
				Code:      structs.DBErrCodeWorkerOffline,
				Message:   msg,
				Retryable: true,
			},
		})
		dbEvent(instance.ID, structs.DBEventFailed, msg, r)
		responder.SendError(w, http.StatusInternalServerError, msg)
		return
	}

	// Register the snapshot schedule with the worker immediately. Without this,
	// a schedule set at create time waited for the next worker reconnect.
	PushDbSchedule(h.WorkerHub, instance)

	logAudit(r, "create", "database_instance", intPtr(instance.ID), strPtr(instance.Name))
	responder.NewCreated(w, instance, "database instance created")
}

// beginDeleteWithFinalSnapshot takes a last backup and defers the teardown until
// it completes.
//
// The ordering is the entire feature. Destroying the volume and *then* taking a
// snapshot is not a final snapshot, and taking both concurrently races the dump
// against the removal. So this marks `pending_final_snapshot`, starts the
// snapshot, and returns; the snapshot's completion is what triggers the actual
// removal (see finaliseDeleteAfterSnapshot). If the snapshot fails, the database
// is still here — which is the correct outcome, since the operator asked not to
// lose it without a copy.
func (h *DatabaseHandler) beginDeleteWithFinalSnapshot(w http.ResponseWriter, r *http.Request, instance *structs.DatabaseInstance) {
	if instance.BackupDestinationID == nil {
		responder.SendError(w, http.StatusBadRequest,
			"a final snapshot needs a backup destination; configure one, or delete without final_snapshot")
		return
	}
	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusConflict,
			fmt.Sprintf("worker %d is not connected, so a final snapshot cannot be taken", instance.WorkerID))
		return
	}
	if instance.Status != string(structs.DBStatusRunning) {
		responder.SendError(w, http.StatusConflict,
			fmt.Sprintf("database is %s — a final snapshot can only be taken from a running instance", instance.Status))
		return
	}

	snapshot, err := h.startSnapshot(instance, "final")
	if err != nil {
		responder.QueryError(w, err, "failed to start the final snapshot")
		return
	}

	pending := true
	if _, err := query.UpdateDatabaseInstance(db.DB, instance.ID, query.UpdateDatabaseInstanceRequest{
		PendingFinalSnapshot: &pending,
	}); err != nil {
		responder.QueryError(w, err, "failed to record the pending delete")
		return
	}

	dbEvent(instance.ID, structs.DBEventRequested,
		fmt.Sprintf("delete requested, pending final snapshot %s", snapshot.Filename), r)
	logAudit(r, "delete", "database_instance", intPtr(instance.ID), strPtr(instance.Name))

	responder.New(w, snapshot,
		"final snapshot started; the database will be destroyed once it completes")
}

// errScheduleNeedsDestination is the message for a snapshot schedule that has
// nowhere to write.
//
// A cron with no backup destination is silently unschedulable: the runner is
// only told about instances that have one, so the schedule saves cleanly, shows
// in the UI, and can never fire. That is the same class of defect as every
// other bug this subsystem has had — configuration that compiles, persists, and
// does nothing — so it is refused at the boundary instead.
const errScheduleNeedsDestination = "a snapshot schedule requires a backup destination; set backup_destination_id, or clear snapshot_schedule"

// validateSnapshotSchedulable reports whether the resulting instance state has a
// schedule without a destination. Returns "" when the combination is valid.
func validateSnapshotSchedulable(schedule *string, destinationID *int) string {
	if schedule == nil || *schedule == "" {
		return ""
	}
	if destinationID == nil {
		return errScheduleNeedsDestination
	}
	return ""
}

// HandleUpdateDatabaseInstance updates a database instance by ID.
func (h *DatabaseHandler) HandleUpdateDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	var body struct {
		Name                *string  `json:"name"`
		Status              *string  `json:"status"`
		Port                *int     `json:"port"`
		RootPassword        *string  `json:"root_password"`
		Password            *string  `json:"password"`
		CPULimit            *float64 `json:"cpu_limit"`
		MemoryLimit         *int     `json:"memory_limit"`
		HealthStatus        *string  `json:"health_status"`
		SnapshotSchedule    *string  `json:"snapshot_schedule"`
		RetentionCount      *int     `json:"retention_count"`
		BackupDestinationID *int     `json:"backup_destination_id"`
		Active              *bool    `json:"active"`
		DeletionProtection  *bool    `json:"deletion_protection"`
	}
	// Read the body once, then decode it twice: into the struct for values, and
	// into raw fields to tell an explicit JSON null from an omitted key. Pointer
	// fields cannot distinguish those, which is why a schedule could be set but
	// never cleared.
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		responder.BadBody(w, err)
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		responder.BadBody(w, err)
		return
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		responder.BadBody(w, err)
		return
	}
	isExplicitNull := func(key string) bool {
		v, ok := present[key]
		return ok && string(v) == "null"
	}
	clearSchedule := isExplicitNull("snapshot_schedule")
	clearRetention := isExplicitNull("retention_count")
	clearDestination := isExplicitNull("backup_destination_id")

	// Status and health are a closed vocabulary. They used to accept any
	// string, which meant a typo could put an instance into a state no part of
	// the platform knows how to render or reconcile.
	if body.Status != nil && !structs.DatabaseStatus(*body.Status).IsValid() {
		responder.SendError(w, http.StatusBadRequest,
			"status must be one of: pending, provisioning, running, stopped, restarting, degraded, deleting, error")
		return
	}
	if body.HealthStatus != nil && !structs.DatabaseHealth(*body.HealthStatus).IsValid() {
		responder.SendError(w, http.StatusBadRequest,
			"health_status must be one of: none, starting, healthy, unhealthy")
		return
	}

	// Moving an instance to a new host port must respect the same ledger the
	// create path does.
	if body.Port != nil {
		existing, getErr := query.GetDatabaseInstanceByID(db.DB, id)
		if getErr != nil {
			responder.NotFound(w)
			return
		}
		conflict, convErr := query.FindPortConflict(db.DB, existing.WorkerID, *body.Port, id)
		if convErr != nil {
			responder.QueryError(w, convErr, "failed to check port availability")
			return
		}
		if conflict != nil {
			responder.SendError(w, http.StatusConflict,
				fmt.Sprintf("port %d is already in use on worker %d by %s %q",
					*body.Port, existing.WorkerID, conflict.Kind, conflict.Name))
			return
		}
	}

	// Validate the state the instance will actually be in, not just the fields
	// supplied: setting a schedule on an instance that has no destination is as
	// unschedulable as sending both together.
	existing, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}
	effectiveSchedule := existing.SnapshotSchedule
	if clearSchedule {
		effectiveSchedule = nil
	} else if body.SnapshotSchedule != nil {
		effectiveSchedule = body.SnapshotSchedule
	}
	effectiveDestination := existing.BackupDestinationID
	if clearDestination {
		effectiveDestination = nil
	} else if body.BackupDestinationID != nil {
		effectiveDestination = body.BackupDestinationID
	}
	if msg := validateSnapshotSchedulable(effectiveSchedule, effectiveDestination); msg != "" {
		responder.SendError(w, http.StatusBadRequest, msg)
		return
	}

	instance, err := query.UpdateDatabaseInstance(db.DB, id, query.UpdateDatabaseInstanceRequest{
		Name:                body.Name,
		Status:              body.Status,
		Port:                body.Port,
		RootPassword:        body.RootPassword,
		Password:            body.Password,
		CPULimit:            body.CPULimit,
		MemoryLimit:         body.MemoryLimit,
		HealthStatus:        body.HealthStatus,
		SnapshotSchedule:    body.SnapshotSchedule,
		RetentionCount:      body.RetentionCount,
		BackupDestinationID: body.BackupDestinationID,
		Active:              body.Active,

		DeletionProtection: body.DeletionProtection,

		ClearSnapshotSchedule:  clearSchedule,
		ClearRetentionCount:    clearRetention,
		ClearBackupDestination: clearDestination,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update database instance")
		return
	}

	// A schedule change has to reach the runner now. The only sender used to be
	// the worker-reconnect sync, so editing a schedule updated a row and changed
	// nothing until the worker happened to reconnect.
	PushDbSchedule(h.WorkerHub, instance)

	logAudit(r, "update", "database_instance", intPtr(id), nil)
	responder.New(w, instance, "database instance updated")
}

// HandleDeleteDatabaseInstance destroys a database for good: the container and
// its data volume are removed on the worker, and the row is retired only once
// the worker confirms both are gone.
//
// Deletion is deliberately deferred rather than optimistic. The row used to be
// soft-deleted the instant the command was queued, which meant a worker that
// failed the removal — or was never asked, because it was offline — left an
// orphaned container and a full data volume with nothing left in the control
// plane pointing at them. Snapshots are never touched: they are the only
// recovery path once the volume is gone.
//
// ?force=true is the escape hatch for a worker that is gone for good: it
// retires the record and records that the on-disk resources were abandoned.
func (h *DatabaseHandler) HandleDeleteDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get database instance")
		return
	}

	// Deletion protection is checked before anything else, including force: its
	// whole purpose is that no single request can destroy the data.
	if instance.DeletionProtection {
		responder.SendError(w, http.StatusConflict, fmt.Sprintf(
			"database %q has deletion protection enabled; turn it off in settings before deleting", instance.Name))
		return
	}

	// A final snapshot has to complete *before* the volume is destroyed, so the
	// delete is staged: take the snapshot, record the intent, and let the
	// snapshot's completion drive the teardown.
	if r.URL.Query().Get("final_snapshot") == "true" {
		h.beginDeleteWithFinalSnapshot(w, r, instance)
		return
	}

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		if r.URL.Query().Get("force") != "true" {
			responder.SendError(w, http.StatusConflict, fmt.Sprintf(
				"worker %d is not connected, so container %s and data volume %s cannot be destroyed; retry once it reconnects, or pass ?force=true to retire the record and abandon them",
				instance.WorkerID, instance.ContainerName, instance.VolumeName))
			return
		}

		log.Printf("delete database instance %d: forced while worker %d offline, container %s and volume %s left in place",
			id, instance.WorkerID, instance.ContainerName, instance.VolumeName)
		dbEvent(id, structs.DBEventRequested, fmt.Sprintf(
			"delete forced while worker %d was offline — container %s and data volume %s abandoned on the worker",
			instance.WorkerID, instance.ContainerName, instance.VolumeName), r)

		if err := query.DeleteDatabaseInstance(db.DB, id); err != nil {
			responder.QueryError(w, err, "failed to delete database instance")
			return
		}

		logAudit(r, "delete", "database_instance", intPtr(id), strPtr(instance.Name))
		responder.New(w, nil, "database instance record deleted; container and data volume left on the offline worker")
		return
	}

	payload := dbCommandPayload(id, socket.MsgDbRemove)
	payload["container_name"] = instance.ContainerName
	payload["volume_name"] = instance.VolumeName
	// The one difference from the `remove` action: a delete takes the data with
	// it. The worker retires the row for us when it confirms.
	payload["remove_volume"] = true

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbRemove,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to send delete command to worker %d: %v", instance.WorkerID, err))
		return
	}

	dbEvent(id, structs.DBEventRequested, "delete requested — container and data volume", r)
	if h.Lifecycle != nil {
		h.Lifecycle.BeginDeleting(id, dbActor(r))
	}

	logAudit(r, "delete", "database_instance", intPtr(id), strPtr(instance.Name))
	responder.New(w, nil, "deleting database: destroying container and data volume")
}

// HandleDatabaseAction dispatches start/stop/restart/remove actions for a
// database instance to the appropriate worker.
func (h *DatabaseHandler) HandleDatabaseAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	// Extract the action from the last segment of the URL path.
	parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	action := parts[len(parts)-1]

	var msgType string
	switch action {
	case "start":
		msgType = socket.MsgDbStart
	case "stop":
		msgType = socket.MsgDbStop
	case "restart":
		msgType = socket.MsgDbRestart
	case "remove":
		msgType = socket.MsgDbRemove
	default:
		responder.SendError(w, http.StatusBadRequest, fmt.Sprintf("unknown action: %s", action))
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	payload := dbCommandPayload(id, msgType)
	payload["container_name"] = instance.ContainerName
	if msgType == socket.MsgDbRemove {
		payload["volume_name"] = instance.VolumeName
	}

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    msgType,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send %s command: %v", action, err))
		return
	}

	dbEvent(id, structs.DBEventRequested, action+" requested", r)

	logAudit(r, action, "database_instance", intPtr(id), strPtr(instance.Name))
	responder.New(w, nil, fmt.Sprintf("database instance %s command sent", action))
}

// HandleGetDatabaseConnection returns everything needed to connect *except* the
// secrets: host, port, database and the application username. Safe for the
// detail page to render on load.
func (h *DatabaseHandler) HandleGetDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	worker, err := query.GetWorkerByID(db.DB, instance.WorkerID)
	if err != nil {
		responder.QueryError(w, err, "failed to get worker")
		return
	}

	responder.New(w, map[string]any{
		"host":          worker.Hostname,
		"port":          instance.Port,
		"database_name": instance.DatabaseName,
		"username":      instance.Username,
		"engine":        instance.Engine,
	}, "database connection details retrieved")
}

// HandleRevealDatabaseCredentials returns live secrets for an instance.
//
// This is deliberately a POST with its own audit trail rather than a field on
// the instance GET: credentials should leave the control plane only when
// somebody explicitly asks for them, and every such request should be
// attributable. Root credentials are included only when explicitly requested,
// since routine use should go through the scoped application user.
func (h *DatabaseHandler) HandleRevealDatabaseCredentials(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	var body struct {
		IncludeRoot bool `json:"include_root"`
	}
	// An empty body is valid and means "application credentials only".
	_ = json.NewDecoder(r.Body).Decode(&body)

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	worker, err := query.GetWorkerByID(db.DB, instance.WorkerID)
	if err != nil {
		responder.QueryError(w, err, "failed to get worker")
		return
	}

	// Passwords are already decrypted by the query layer.
	password := ""
	if instance.Password != nil {
		password = *instance.Password
	}

	var connString string
	switch instance.Engine {
	case "mysql", "mariadb":
		connString = fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	case "postgres":
		connString = fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	}

	payload := map[string]any{
		"username":          instance.Username,
		"password":          password,
		"connection_string": connString,
		"host":              worker.Hostname,
		"port":              instance.Port,
		"database_name":     instance.DatabaseName,
	}

	detail := "application credentials revealed"
	if body.IncludeRoot {
		rootPassword := ""
		if instance.RootPassword != nil {
			rootPassword = *instance.RootPassword
		}
		payload["root_password"] = rootPassword
		detail = "root credentials revealed"
	}

	dbEvent(id, structs.DBEventReveal, detail, r)
	logAudit(r, "reveal_credentials", "database_instance", intPtr(id), strPtr(detail))

	responder.New(w, payload, "database credentials retrieved")
}

// HandleGetDatabaseCredentials is the legacy credentials endpoint.
//
// Deprecated: it returns root credentials from a plain GET, so any request that
// merely reads an instance leaks them. Kept working for existing clients;
// use POST /database-instances/{id}/reveal instead, which is audited.
func (h *DatabaseHandler) HandleGetDatabaseCredentials(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	worker, err := query.GetWorkerByID(db.DB, instance.WorkerID)
	if err != nil {
		responder.QueryError(w, err, "failed to get worker")
		return
	}

	rootPassword := ""
	if instance.RootPassword != nil {
		rootPassword = *instance.RootPassword
	}
	password := ""
	if instance.Password != nil {
		password = *instance.Password
	}

	var connString string
	switch instance.Engine {
	case "mysql", "mariadb":
		connString = fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	case "postgres":
		connString = fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	}

	dbEvent(id, structs.DBEventReveal, "credentials read via deprecated GET endpoint", r)
	logAudit(r, "reveal_credentials", "database_instance", intPtr(id), strPtr("legacy GET endpoint"))

	responder.New(w, map[string]any{
		"root_password":     rootPassword,
		"username":          instance.Username,
		"password":          password,
		"connection_string": connString,
		"host":              worker.Hostname,
		"port":              instance.Port,
	}, "database credentials retrieved")
}
