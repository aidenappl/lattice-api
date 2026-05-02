package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

// HandleListBackupDestinations returns all active backup destinations.
func HandleListBackupDestinations(w http.ResponseWriter, r *http.Request) {
	destinations, err := query.ListBackupDestinations(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list backup destinations")
		return
	}

	responder.New(w, destinations, "backup destinations retrieved")
}

// HandleGetBackupDestination returns a single backup destination by ID.
func HandleGetBackupDestination(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid backup destination id")
		return
	}

	destination, err := query.GetBackupDestinationByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	responder.New(w, destination, "backup destination retrieved")
}

// HandleCreateBackupDestination creates a new backup destination.
func HandleCreateBackupDestination(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string         `json:"name"`
		Type   string         `json:"type"`
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if body.Type == "" {
		responder.MissingBodyFields(w, "type")
		return
	}

	if body.Type != "s3" && body.Type != "google_drive" && body.Type != "samba" {
		responder.SendError(w, http.StatusBadRequest, "type must be one of: s3, google_drive, samba")
		return
	}

	configJSON := ""
	if body.Config != nil {
		b, err := json.Marshal(body.Config)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, "failed to marshal config")
			return
		}
		configJSON = string(b)
	}

	destination, err := query.CreateBackupDestination(db.DB, query.CreateBackupDestinationRequest{
		Name:   body.Name,
		Type:   body.Type,
		Config: configJSON,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create backup destination")
		return
	}

	logAudit(r, "create", "backup_destination", intPtr(destination.ID), strPtr(destination.Name))
	responder.NewCreated(w, destination, "backup destination created")
}

// HandleUpdateBackupDestination updates an existing backup destination.
func HandleUpdateBackupDestination(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid backup destination id")
		return
	}

	var body struct {
		Name   *string         `json:"name"`
		Type   *string         `json:"type"`
		Config *map[string]any `json:"config"`
		Active *bool           `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	if body.Type != nil {
		switch *body.Type {
		case "s3", "google_drive", "samba":
		default:
			responder.SendError(w, http.StatusBadRequest, "type must be s3, google_drive, or samba")
			return
		}
	}

	req := query.UpdateBackupDestinationRequest{
		Name:   body.Name,
		Type:   body.Type,
		Active: body.Active,
	}

	if body.Config != nil {
		b, err := json.Marshal(*body.Config)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, "failed to marshal config")
			return
		}
		configStr := string(b)
		req.Config = &configStr
	}

	destination, err := query.UpdateBackupDestination(db.DB, id, req)
	if err != nil {
		responder.QueryError(w, err, "failed to update backup destination")
		return
	}

	logAudit(r, "update", "backup_destination", intPtr(id), nil)
	responder.New(w, destination, "backup destination updated")
}

// HandleDeleteBackupDestination soft-deletes a backup destination.
func HandleDeleteBackupDestination(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid backup destination id")
		return
	}

	if err := query.DeleteBackupDestination(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete backup destination")
		return
	}

	logAudit(r, "delete", "backup_destination", intPtr(id), nil)
	responder.New(w, nil, "backup destination deleted")
}

// HandleTestBackupDestination sends a test command to a worker to verify
// connectivity to a backup destination.
func (h *DatabaseHandler) HandleTestBackupDestination(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid backup destination id")
		return
	}

	workerIDStr := r.URL.Query().Get("worker_id")
	if workerIDStr == "" {
		responder.SendError(w, http.StatusBadRequest, "worker_id query parameter required")
		return
	}
	workerID, err := strconv.Atoi(workerIDStr)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker_id")
		return
	}

	destination, err := query.GetBackupDestinationByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	payload := map[string]any{
		"backup_destination_id": destination.ID,
		"type":                  destination.Type,
	}
	if destination.Config != nil {
		var configMap map[string]any
		if err := json.Unmarshal([]byte(*destination.Config), &configMap); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to parse backup destination config")
			return
		}
		payload["config"] = configMap
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type:    socket.MsgBackupDestTest,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send test command: %v", err))
		return
	}

	responder.New(w, nil, "test command sent")
}
