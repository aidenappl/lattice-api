package routers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
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

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	snapshot, err := h.startSnapshot(instance, "manual")
	if err != nil {
		responder.QueryError(w, err, "failed to create snapshot")
		return
	}

	dbEvent(instance.ID, structs.DBEventRequested, "snapshot requested: "+snapshot.Filename, r)
	logAudit(r, "create", "database_snapshot", intPtr(snapshot.ID), strPtr(instance.Name))
	responder.NewCreated(w, snapshot, "snapshot created")
}

// startSnapshot creates the snapshot row and dispatches the dump to the worker.
//
// Shared by the manual snapshot endpoint and the final-snapshot-on-delete path,
// so both produce an identical artifact and an identical row — the only
// difference is trigger_type, which is what later tells an operator why a
// snapshot exists.
func (h *DatabaseHandler) startSnapshot(instance *structs.DatabaseInstance, trigger string) (*structs.DatabaseSnapshot, error) {
	destination, err := query.GetBackupDestinationByID(db.DB, *instance.BackupDestinationID)
	if err != nil {
		return nil, err
	}

	filename := fmt.Sprintf("%s_%s_%s.sql.gz", instance.ContainerName, instance.DatabaseName,
		time.Now().UTC().Format("20060102T150405Z"))

	snapshot, err := query.CreateSnapshot(db.DB, query.CreateSnapshotRequest{
		DatabaseInstanceID:  instance.ID,
		BackupDestinationID: instance.BackupDestinationID,
		Filename:            filename,
		Engine:              instance.Engine,
		DatabaseName:        instance.DatabaseName,
		TriggerType:         trigger,
	})
	if err != nil {
		return nil, err
	}

	payload := dbCommandPayload(instance.ID, socket.MsgDbSnapshot)
	payload[socket.PayloadSnapshotID] = snapshot.ID
	payload[socket.PayloadContainerName] = instance.ContainerName
	payload[socket.PayloadEngine] = instance.Engine
	payload[socket.PayloadDatabaseName] = instance.DatabaseName
	payload[socket.PayloadUsername] = instance.Username
	payload[socket.PayloadFilename] = filename
	if instance.Password != nil {
		payload[socket.PayloadPassword] = *instance.Password
	}

	destPayload := map[string]any{socket.PayloadDestType: destination.Type}
	if destination.Config != nil {
		var configMap map[string]any
		if err := json.Unmarshal([]byte(*destination.Config), &configMap); err != nil {
			return nil, fmt.Errorf("backup destination %d has unparseable config: %w", destination.ID, err)
		}
		destPayload[socket.PayloadDestConfig] = configMap
	}
	payload[socket.PayloadBackupDestination] = destPayload

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbSnapshot,
		Payload: payload,
	}); err != nil {
		return nil, fmt.Errorf("failed to send snapshot command: %w", err)
	}

	return snapshot, nil
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

	payload := dbCommandPayload(instance.ID, socket.MsgDbRestore)
	payload["snapshot_id"] = snapshot.ID
	payload["container_name"] = instance.ContainerName
	payload["engine"] = instance.Engine
	payload["database_name"] = instance.DatabaseName
	payload["username"] = instance.Username
	payload["filename"] = snapshot.Filename
	payload["backup_destination"] = map[string]any{
		"type": destination.Type,
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

	dbEvent(instance.ID, structs.DBEventRequested, "restore requested from "+snapshot.Filename, r)
	logAudit(r, "restore", "database_snapshot", intPtr(snapshot.ID), strPtr(instance.Name))
	responder.New(w, nil, "restore command sent")
}

// DeleteSnapshotArtifact retires a snapshot row and asks its worker to remove
// the backing file from the remote destination.
//
// Shared by the HTTP delete handler and by retention enforcement, so an operator
// deleting a snapshot and retention expiring one take exactly the same path —
// including the remote delete. Remote cleanup is best-effort: a failure is logged
// and does not block retiring the row, because a row that outlives its file is
// worse than a file that outlives its row (the first is invisible, the second is
// findable). `instance` may be nil when it can no longer be resolved.
func DeleteSnapshotArtifact(hub *socket.WorkerHub, snapshot *structs.DatabaseSnapshot, instance *structs.DatabaseInstance) error {
	if snapshot == nil {
		return nil
	}

	if snapshot.BackupDestinationID != nil && instance != nil && hub != nil {
		destination, destErr := query.GetBackupDestinationByID(db.DB, *snapshot.BackupDestinationID)
		switch {
		case destErr != nil:
			log.Printf("delete snapshot %d: cannot resolve destination, remote file %q left in place",
				snapshot.ID, snapshot.Filename)
		case !hub.IsConnected(instance.WorkerID):
			log.Printf("delete snapshot %d: worker %d offline, remote file %q left in place",
				snapshot.ID, instance.WorkerID, snapshot.Filename)
		default:
			payload := dbCommandPayload(instance.ID, socket.MsgDbDeleteSnapshot)
			payload[socket.PayloadSnapshotID] = snapshot.ID
			payload[socket.PayloadFilename] = snapshot.Filename
			destPayload := map[string]any{socket.PayloadDestType: destination.Type}
			if destination.Config != nil {
				var configMap map[string]any
				if jsonErr := json.Unmarshal([]byte(*destination.Config), &configMap); jsonErr == nil {
					destPayload[socket.PayloadDestConfig] = configMap
				}
			}
			payload[socket.PayloadBackupDestination] = destPayload

			if sendErr := hub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
				Type:    socket.MsgDbDeleteSnapshot,
				Payload: payload,
			}); sendErr != nil {
				log.Printf("delete snapshot %d: failed to send remote delete to worker %d: %v",
					snapshot.ID, instance.WorkerID, sendErr)
			}
		}
	}

	return query.DeleteSnapshot(db.DB, snapshot.ID)
}

// HandleDeleteSnapshot soft-deletes a database snapshot and instructs the
// worker to remove the backing file from its remote destination.
//
// The remote delete used to be missing entirely: the row was soft-deleted and
// the file was left behind on S3/Drive/Samba indefinitely, silently accruing
// storage cost with no way to find the orphans afterwards.
func (h *DatabaseHandler) HandleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid snapshot id")
		return
	}

	snapshot, err := query.GetSnapshotByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	instance, instErr := query.GetDatabaseInstanceByID(db.DB, snapshot.DatabaseInstanceID)
	if instErr != nil {
		log.Printf("delete snapshot %d: cannot resolve instance, remote file %q left in place", id, snapshot.Filename)
		instance = nil
	}

	if err := DeleteSnapshotArtifact(h.WorkerHub, snapshot, instance); err != nil {
		responder.QueryError(w, err, "failed to delete snapshot")
		return
	}

	logAudit(r, "delete", "database_snapshot", intPtr(id), strPtr(snapshot.Filename))
	responder.New(w, nil, "snapshot deleted")
}
