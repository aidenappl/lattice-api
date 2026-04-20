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

type NetworkHandler struct {
	WorkerHub *socket.WorkerHub
}

func (h *NetworkHandler) HandleListNetworks(w http.ResponseWriter, r *http.Request) {
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

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type:    socket.MsgListNetworks,
		Payload: map[string]any{},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send list_networks command: %v", err))
		return
	}

	responder.New(w, nil, "list_networks command sent to worker")
}

func (h *NetworkHandler) HandleCreateNetwork(w http.ResponseWriter, r *http.Request) {
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

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type: socket.MsgCreateNetwork,
		Payload: map[string]any{
			"name":   body.Name,
			"driver": body.Driver,
		},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send create_network command: %v", err))
		return
	}

	logAudit(r, "create network", "network", intPtr(workerID), strPtr(body.Name))
	responder.New(w, nil, "create_network command sent to worker")
}

func (h *NetworkHandler) HandleDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	networkName := mux.Vars(r)["name"]
	if networkName == "" {
		responder.SendError(w, http.StatusBadRequest, "network name is required")
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

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type: socket.MsgRemoveNetwork,
		Payload: map[string]any{
			"name": networkName,
		},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send remove_network command: %v", err))
		return
	}

	logAudit(r, "delete network", "network", intPtr(workerID), strPtr(networkName))
	responder.New(w, nil, "remove_network command sent to worker")
}
