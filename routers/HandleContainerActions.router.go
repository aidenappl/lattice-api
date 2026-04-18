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

type ContainerActionHandler struct {
	WorkerHub *socket.WorkerHub
}

func (h *ContainerActionHandler) HandleStopContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgStop)
}

func (h *ContainerActionHandler) HandleRestartContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgRestart)
}

func (h *ContainerActionHandler) HandleRemoveContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgRemove)
}

func (h *ContainerActionHandler) HandleRecreateContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgRecreate)
}

func (h *ContainerActionHandler) sendContainerAction(w http.ResponseWriter, r *http.Request, action string) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	container, err := query.GetContainerByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	stack, err := query.GetStackByID(db.DB, container.StackID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if stack.WorkerID == nil {
		responder.SendError(w, http.StatusBadRequest, "stack has no worker assigned")
		return
	}

	if !h.WorkerHub.IsConnected(*stack.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
		Type: action,
		Payload: map[string]any{
			"container_name": container.Name,
			"container_id":   container.ID,
		},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send %s command: %v", action, err))
		return
	}

	responder.New(w, nil, fmt.Sprintf("container %s command sent", action))
}
