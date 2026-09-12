package routers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

type ContainerActionHandler struct {
	WorkerHub *socket.WorkerHub
}

func (h *ContainerActionHandler) HandleStartContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgStart)
}

func (h *ContainerActionHandler) HandleStopContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgStop)
}

func (h *ContainerActionHandler) HandleKillContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgKill)
}

func (h *ContainerActionHandler) HandleRestartContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgRestart)
}

func (h *ContainerActionHandler) HandlePauseContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgPause)
}

func (h *ContainerActionHandler) HandleUnpauseContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgUnpause)
}

func (h *ContainerActionHandler) HandleRemoveContainer(w http.ResponseWriter, r *http.Request) {
	h.sendContainerAction(w, r, socket.MsgRemove)
}

func (h *ContainerActionHandler) HandleRecreateContainer(w http.ResponseWriter, r *http.Request) {
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

	if err := h.RecreateContainer(container, *stack.WorkerID); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send recreate command: %v", err))
		return
	}

	logAudit(r, "recreate", "container", intPtr(id), strPtr(container.Name))
	responder.New(w, nil, "container recreate command sent")
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

	logAudit(r, action, "container", intPtr(id), strPtr(container.Name))
	responder.New(w, nil, fmt.Sprintf("container %s command sent", action))
}

// RecreateContainer sends a recreate (pull image:tag, then replace) for one
// container to a worker. The worker accepts the command; the runner performs the
// pull and replace asynchronously.
//
// It satisfies automations.ContainerRedeployer, so an automation's
// redeploy_container step sends exactly the command the Recreate button sends.
func (h *ContainerActionHandler) RecreateContainer(container *structs.Container, workerID int) error {
	return h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type:    socket.MsgRecreate,
		Payload: recreateContainerPayload(container),
	})
}

// recreateContainerPayload builds the recreate payload for one container, with
// registry auth resolved so the runner can pull before recreating: by the
// container's own registry when it names one, otherwise by matching the image's
// hostname against every configured registry (the same logic as deploy).
//
// One builder for every path that recreates a single container — the Recreate
// button, a deploy token's ?container=, and automations. The first two were
// identical inline copies; automations would have made a third.
func recreateContainerPayload(container *structs.Container) map[string]any {
	payload := map[string]any{
		"container_name": container.Name,
		"container_id":   container.ID,
		"image":          container.Image,
		"tag":            container.Tag,
	}

	if container.RegistryID != nil {
		registry, regErr := query.GetRegistryByID(db.DB, *container.RegistryID)
		if regErr == nil && registry != nil && registry.Username != nil && registry.Password != nil {
			payload["auth"] = map[string]any{
				"username": *registry.Username,
				"password": *registry.Password,
			}
		}
		return payload
	}

	allRegistries, _ := query.ListRegistries(db.DB)
	if allRegistries != nil {
		for _, reg := range *allRegistries {
			regHost := strings.TrimPrefix(strings.TrimPrefix(reg.URL, "https://"), "http://")
			regHost = strings.TrimSuffix(regHost, "/")
			if strings.HasPrefix(container.Image, regHost+"/") || container.Image == regHost {
				if reg.Username != nil && reg.Password != nil {
					payload["auth"] = map[string]any{
						"username": *reg.Username,
						"password": *reg.Password,
					}
				}
				break
			}
		}
	}
	return payload
}
