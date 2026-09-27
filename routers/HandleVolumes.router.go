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

type VolumeHandler struct {
	WorkerHub *socket.WorkerHub
}

func (h *VolumeHandler) HandleListVolumes(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	_, err = query.GetWorkerByID(db.DB, workerID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(r.Context(), socket.MsgListVolumes, map[string]any{})); err != nil {
		sendDispatchError(w, "list_volumes", err)
		return
	}

	responder.New(w, nil, "list_volumes command sent to worker")
}

func (h *VolumeHandler) HandleCreateVolume(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	_, err = query.GetWorkerByID(db.DB, workerID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	var body struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(r.Context(), socket.MsgCreateVolume, map[string]any{
		"name":   body.Name,
		"driver": body.Driver,
	})); err != nil {
		sendDispatchError(w, "create_volume", err)
		return
	}

	logAudit(r, "create volume", "volume", intPtr(workerID), strPtr(body.Name))
	responder.New(w, nil, "create_volume command sent to worker")
}

func (h *VolumeHandler) HandleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	volumeName := mux.Vars(r)["name"]
	if volumeName == "" {
		responder.SendError(w, http.StatusBadRequest, "volume name is required")
		return
	}

	_, err = query.GetWorkerByID(db.DB, workerID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(r.Context(), socket.MsgRemoveVolume, map[string]any{
		"name":  volumeName,
		"force": r.URL.Query().Get("force") == "true",
	})); err != nil {
		sendDispatchError(w, "remove_volume", err)
		return
	}

	logAudit(r, "delete volume", "volume", intPtr(workerID), strPtr(volumeName))
	responder.New(w, nil, "remove_volume command sent to worker")
}
