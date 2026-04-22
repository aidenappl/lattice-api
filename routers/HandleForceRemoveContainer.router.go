package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

// HandleForceRemoveContainer sends a force-remove command to a worker via WebSocket.
// POST /admin/workers/{id}/force-remove
// Body: {"container_name": "some-container-retired-1234567890"}
func (h *ContainerActionHandler) HandleForceRemoveContainer(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	if !h.WorkerHub.IsConnected(workerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	var body struct {
		ContainerName string `json:"container_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ContainerName == "" {
		responder.SendError(w, http.StatusBadRequest, "container_name is required")
		return
	}

	if err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type: socket.MsgForceRemove,
		Payload: map[string]any{
			"container_name": body.ContainerName,
		},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send force-remove command: %v", err))
		return
	}

	logAudit(r, "force_remove", "container", nil, strPtr(body.ContainerName))
	responder.New(w, nil, "force-remove command sent")
}
