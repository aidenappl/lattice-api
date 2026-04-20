package routers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

func (h *ContainerActionHandler) HandleDeleteContainer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	container, err := query.GetContainerByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get container")
		return
	}

	// Best-effort: send remove command to the worker
	stack, err := query.GetStackByID(db.DB, container.StackID)
	if err == nil && stack.WorkerID != nil && h.WorkerHub.IsConnected(*stack.WorkerID) {
		if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
			Type: socket.MsgRemove,
			Payload: map[string]any{
				"container_name": container.Name,
				"container_id":   container.ID,
			},
		}); err != nil {
			log.Printf("delete container %d: failed to send remove to worker %d: %v", id, *stack.WorkerID, err)
		}
	}

	if err := query.DeleteContainer(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete container")
		return
	}

	logAudit(r, "delete", "container", intPtr(id), strPtr(container.Name))
	responder.New(w, nil, "container deleted")
}
