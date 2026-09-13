package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

func (h *ContainerActionHandler) HandleDeleteStack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	// Fetch the stack to get worker ID
	stack, err := query.GetStackByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get stack")
		return
	}

	// Fetch all containers on the stack and remove them from the worker
	containers, err := query.ListContainersByStack(db.DB, id)
	if err != nil {
		logger.Error("stack", "delete: failed to list containers", logger.F{"stack_id": id, "error": err})
	} else if containers != nil && stack.WorkerID != nil && h.WorkerHub.IsConnected(*stack.WorkerID) {
		for _, c := range *containers {
			if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
				Type: socket.MsgRemove,
				Payload: map[string]any{
					"container_name": c.Name,
					"container_id":   c.ID,
				},
			}); err != nil {
				logger.Warn("stack", "delete: failed to send remove for container", logger.F{"stack_id": id, "container": c.Name, "error": err})
			} else {
				logger.Info("stack", "delete: sent remove for container", logger.F{"stack_id": id, "container": c.Name, "worker_id": *stack.WorkerID})
			}
		}
	}

	tx, txErr := db.BeginTx()
	if txErr != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback()

	// Fail any in-progress deployments so they don't stay stuck in "deploying"
	if err := query.FailActiveDeployments(tx, id); err != nil {
		responder.QueryError(w, err, "failed to cancel active deployments")
		return
	}

	if err := query.DeleteStack(tx, id); err != nil {
		responder.QueryError(w, err, "failed to delete stack")
		return
	}

	if err := tx.Commit(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to commit delete")
		return
	}

	logAudit(r, "delete", "stack", intPtr(id), strPtr(stack.Name))
	responder.New(w, nil, "stack deleted")
}
