package routers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

type WorkerActionHandler struct {
	WorkerHub *socket.WorkerHub
}

func (h *WorkerActionHandler) HandleRebootWorker(w http.ResponseWriter, r *http.Request) {
	h.sendWorkerAction(w, r, socket.MsgRebootOS, "reboot")
}

func (h *WorkerActionHandler) HandleUpgradeRunner(w http.ResponseWriter, r *http.Request) {
	h.sendWorkerAction(w, r, socket.MsgUpgradeRunner, "upgrade runner")
}

func (h *WorkerActionHandler) HandleStopAllContainers(w http.ResponseWriter, r *http.Request) {
	h.sendWorkerAction(w, r, socket.MsgStopAll, "stop all containers")
}

func (h *WorkerActionHandler) HandleStartAllContainers(w http.ResponseWriter, r *http.Request) {
	h.sendWorkerAction(w, r, socket.MsgStartAll, "start all containers")
}

func (h *WorkerActionHandler) sendWorkerAction(w http.ResponseWriter, r *http.Request, action string, label string) {
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
		Type:    action,
		Payload: map[string]any{},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send %s command: %v", label, err))
		return
	}

	logAudit(r, label, "worker", intPtr(workerID), nil)
	responder.New(w, nil, fmt.Sprintf("%s command sent to worker", label))
}
