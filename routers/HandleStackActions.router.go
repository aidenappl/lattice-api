package routers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

func (h *ContainerActionHandler) HandleRestartStack(w http.ResponseWriter, r *http.Request) {
	h.stackBulkAction(w, r, socket.MsgRestart, "restart_all",
		func(c structs.Container) bool { return c.Status == "running" })
}

func (h *ContainerActionHandler) HandleStopStack(w http.ResponseWriter, r *http.Request) {
	h.stackBulkAction(w, r, socket.MsgStop, "stop_all",
		func(c structs.Container) bool { return c.Status == "running" })
}

func (h *ContainerActionHandler) HandleStartStack(w http.ResponseWriter, r *http.Request) {
	h.stackBulkAction(w, r, socket.MsgStart, "start_all",
		func(c structs.Container) bool { return c.Status == "stopped" || c.Status == "error" })
}

func (h *ContainerActionHandler) stackBulkAction(
	w http.ResponseWriter, r *http.Request,
	action string, label string,
	filter func(structs.Container) bool,
) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	stack, err := query.GetStackByID(db.DB, stackID)
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

	containers, err := query.ListContainersByStack(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	count := 0
	for _, c := range *containers {
		if !filter(c) {
			continue
		}
		_ = h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
			Type: action,
			Payload: map[string]any{
				"container_name": c.Name,
			},
		})
		count++
	}

	logAudit(r, label, "stack", intPtr(stackID), strPtr(fmt.Sprintf("%d containers", count)))
	responder.New(w, map[string]any{"count": count}, fmt.Sprintf("%s command sent to %d containers", label, count))
}
