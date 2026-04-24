package routers

import (
	"crypto/sha256"
	"encoding/hex"
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

	worker, err := query.GetWorkerByID(db.DB, workerID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	// Prevent duplicate upgrade/reboot commands if an action is already in progress
	if (action == socket.MsgUpgradeRunner || action == socket.MsgRebootOS) && worker.PendingAction != nil {
		responder.SendError(w, http.StatusConflict, "an action is already in progress for this worker")
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	payload := map[string]any{}

	// For upgrade_runner, include the SHA256 hash of the embedded install script
	// so the runner can verify integrity of the downloaded script before executing.
	if action == socket.MsgUpgradeRunner && InstallScript != nil {
		hash := sha256.Sum256(InstallScript)
		payload["expected_hash"] = hex.EncodeToString(hash[:])
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type:    action,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send %s command: %v", label, err))
		return
	}

	// Persist pending action for trackable actions
	if action == socket.MsgUpgradeRunner || action == socket.MsgRebootOS {
		actionData := map[string]string{
			"action":     action,
			"status":     "accepted",
			"started_at": time.Now().UTC().Format(time.RFC3339),
		}
		actionBytes, _ := json.Marshal(actionData)
		actionJSON := string(actionBytes)
		_ = query.SetWorkerPendingAction(db.DB, workerID, &actionJSON)
	}

	logAudit(r, label, "worker", intPtr(workerID), nil)
	responder.New(w, nil, fmt.Sprintf("%s command sent to worker", label))
}
