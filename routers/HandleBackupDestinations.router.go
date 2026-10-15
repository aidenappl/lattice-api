package routers

import (
	"encoding/json"
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
		Name     string         `json:"name"`
		Type     string         `json:"type"`
		Locality string         `json:"locality"`
		Config   map[string]any `json:"config"`
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
		Name:     body.Name,
		Type:     body.Type,
		Locality: body.Locality,
		Config:   configJSON,
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
		Name     *string         `json:"name"`
		Type     *string         `json:"type"`
		Locality *string         `json:"locality"`
		Config   *map[string]any `json:"config"`
		Active   *bool           `json:"active"`
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
		Name:     body.Name,
		Type:     body.Type,
		Locality: body.Locality,
		Active:   body.Active,
	}

	if body.Config != nil {
		// The config is merged into the stored one, not swapped for it. Clients
		// never see the stored config (it holds credentials), so lattice-web's
		// edit form sends only the fields the user filled in — and replacing
		// wholesale turned "change the path prefix" into "delete the bucket,
		// region and keys", silently breaking every backup to that destination.
		existing, err := query.GetBackupDestinationByID(db.DB, id)
		if err != nil {
			responder.NotFound(w)
			return
		}
		sameType := body.Type == nil || *body.Type == existing.Type
		configStr, err := mergeBackupConfig(existing.Config, *body.Config, sameType)
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to merge backup destination config", err)
			return
		}
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

// mergeBackupConfig applies a partial config update to the stored one.
//
// Keys in patch overwrite stored keys; a key set to JSON null removes it, so an
// optional field (endpoint, path prefix) can still be cleared. When the
// destination's type changes the stored keys belong to a different backend
// and are dropped — the patch is the whole new config.
func mergeBackupConfig(stored *string, patch map[string]any, sameType bool) (string, error) {
	merged := map[string]any{}
	if sameType && stored != nil && *stored != "" {
		if err := json.Unmarshal([]byte(*stored), &merged); err != nil {
			return "", err
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
			responder.SendError(w, http.StatusInternalServerError, "failed to parse backup destination config", err)
			return
		}
		payload["config"] = configMap
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(r.Context(), socket.MsgBackupDestTest, payload)); err != nil {
		sendDispatchError(w, "test", err)
		return
	}

	responder.New(w, nil, "test command sent")
}
