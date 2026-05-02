package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

// HandleListSnapshots returns all snapshots for a database instance.
func HandleListSnapshots(w http.ResponseWriter, r *http.Request) {
	instanceID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	snapshots, err := query.ListSnapshotsByInstance(db.DB, instanceID)
	if err != nil {
		responder.QueryError(w, err, "failed to list snapshots")
		return
	}

	responder.New(w, snapshots, "snapshots retrieved")
}

// HandleCreateSnapshot creates a new snapshot for a database instance and sends
// the snapshot command to the worker.
func (h *DatabaseHandler) HandleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	instanceID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if instance.BackupDestinationID == nil {
		responder.SendError(w, http.StatusBadRequest, "no backup destination configured for this database")
		return
	}

	destination, err := query.GetBackupDestinationByID(db.DB, *instance.BackupDestinationID)
	if err != nil {
		responder.QueryError(w, err, "failed to get backup destination")
		return
	}

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	filename := fmt.Sprintf("%s_%s_%s.sql.gz", instance.ContainerName, instance.DatabaseName, time.Now().UTC().Format("20060102T150405Z"))

	snapshot, err := query.CreateSnapshot(db.DB, query.CreateSnapshotRequest{
		DatabaseInstanceID:  instance.ID,
		BackupDestinationID: instance.BackupDestinationID,
		Filename:            filename,
		Engine:              instance.Engine,
		DatabaseName:        instance.DatabaseName,
		TriggerType:         "manual",
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create snapshot")
		return
	}

	payload := map[string]any{
		"snapshot_id":          snapshot.ID,
		"database_instance_id": instance.ID,
		"container_name":       instance.ContainerName,
		"engine":               instance.Engine,
		"database_name":        instance.DatabaseName,
		"username":             instance.Username,
		"filename":             filename,
		"backup_destination": map[string]any{
			"type": destination.Type,
		},
	}
	if instance.Password != nil {
		payload["password"] = *instance.Password
	}
	if destination.Config != nil {
		var configMap map[string]any
		if err := json.Unmarshal([]byte(*destination.Config), &configMap); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to parse backup destination config")
			return
		}
		payload["backup_destination"].(map[string]any)["config"] = configMap
	}

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbSnapshot,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send snapshot command: %v", err))
		return
	}

	logAudit(r, "create", "database_snapshot", intPtr(snapshot.ID), strPtr(instance.Name))
	responder.NewCreated(w, snapshot, "snapshot created")
}

// HandleRestoreSnapshot restores a database instance from a snapshot.
func (h *DatabaseHandler) HandleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	instanceID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	var body struct {
		SnapshotID int `json:"snapshot_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.SnapshotID == 0 {
		responder.MissingBodyFields(w, "snapshot_id")
		return
	}

	snapshot, err := query.GetSnapshotByID(db.DB, body.SnapshotID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if snapshot.DatabaseInstanceID != instanceID {
		responder.SendError(w, http.StatusBadRequest, "snapshot does not belong to this database instance")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, instanceID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if snapshot.BackupDestinationID == nil {
		responder.SendError(w, http.StatusBadRequest, "snapshot has no backup destination")
		return
	}

	destination, err := query.GetBackupDestinationByID(db.DB, *snapshot.BackupDestinationID)
	if err != nil {
		responder.QueryError(w, err, "failed to get backup destination")
		return
	}

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	payload := map[string]any{
		"snapshot_id":          snapshot.ID,
		"database_instance_id": instance.ID,
		"container_name":       instance.ContainerName,
		"engine":               instance.Engine,
		"database_name":        instance.DatabaseName,
		"username":             instance.Username,
		"filename":             snapshot.Filename,
		"backup_destination": map[string]any{
			"type": destination.Type,
		},
	}
	if instance.Password != nil {
		payload["password"] = *instance.Password
	}
	if destination.Config != nil {
		var configMap map[string]any
		if err := json.Unmarshal([]byte(*destination.Config), &configMap); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to parse backup destination config")
			return
		}
		payload["backup_destination"].(map[string]any)["config"] = configMap
	}

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbRestore,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send restore command: %v", err))
		return
	}

	logAudit(r, "restore", "database_snapshot", intPtr(snapshot.ID), strPtr(instance.Name))
	responder.New(w, nil, "restore command sent")
}

// HandleDeleteSnapshot soft-deletes a database snapshot.
func HandleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid snapshot id")
		return
	}

	if err := query.DeleteSnapshot(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete snapshot")
		return
	}

	logAudit(r, "delete", "database_snapshot", intPtr(id), nil)
	responder.New(w, nil, "snapshot deleted")
}
