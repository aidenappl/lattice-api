package routers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

type DeployHandler struct {
	WorkerHub *socket.WorkerHub
	AdminHub  *socket.AdminHub
}

func (h *DeployHandler) HandleDeployStack(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
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

	// Fetch containers for this stack
	containers, err := query.ListContainersByStack(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	// Build container specs with registry auth resolved
	containerSpecs := make([]map[string]any, 0, len(*containers))
	for _, c := range *containers {
		spec := map[string]any{
			"id":             c.ID,
			"name":           c.Name,
			"image":          c.Image,
			"tag":            c.Tag,
			"replicas":       c.Replicas,
			"restart_policy": c.RestartPolicy,
		}

		if c.PortMappings != nil {
			var pm []any
			_ = json.Unmarshal([]byte(*c.PortMappings), &pm)
			spec["port_mappings"] = pm
		}
		if c.EnvVars != nil {
			var ev map[string]any
			_ = json.Unmarshal([]byte(*c.EnvVars), &ev)
			spec["env_vars"] = ev
		}
		if c.Volumes != nil {
			var vol map[string]any
			_ = json.Unmarshal([]byte(*c.Volumes), &vol)
			spec["volumes"] = vol
		}
		if c.CPULimit != nil {
			spec["cpu_limit"] = *c.CPULimit
		}
		if c.MemoryLimit != nil {
			spec["memory_limit"] = *c.MemoryLimit
		}
		if c.Command != nil {
			var cmd []string
			_ = json.Unmarshal([]byte(*c.Command), &cmd)
			spec["command"] = cmd
		}
		if c.Entrypoint != nil {
			var ep []string
			_ = json.Unmarshal([]byte(*c.Entrypoint), &ep)
			spec["entrypoint"] = ep
		}

		// Resolve registry credentials
		if c.RegistryID != nil {
			reg, err := query.GetRegistryByID(db.DB, *c.RegistryID)
			if err == nil && reg != nil {
				auth := map[string]string{}
				if reg.Username != nil {
					auth["username"] = *reg.Username
				}
				if reg.Password != nil {
					auth["password"] = *reg.Password
				}
				if len(auth) > 0 {
					spec["registry_auth"] = auth
				}
			}
		}

		containerSpecs = append(containerSpecs, spec)
	}

	// Create deployment record
	deployment, err := query.CreateDeployment(db.DB, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create deployment")
		return
	}

	deployingStatus := "deploying"
	_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{
		Status: &deployingStatus,
	})

	// Send deploy command to worker
	payload := map[string]any{
		"deployment_id": deployment.ID,
		"stack_name":    stack.Name,
		"strategy":      stack.DeploymentStrategy,
		"containers":    containerSpecs,
	}

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
		Type:    socket.MsgDeploy,
		Payload: payload,
	}); err != nil {
		log.Printf("failed to send deploy command to worker=%d: %v", *stack.WorkerID, err)
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send deploy command: %v", err))
		return
	}

	responder.NewCreated(w, deployment, "deployment created and sent to worker")
}
