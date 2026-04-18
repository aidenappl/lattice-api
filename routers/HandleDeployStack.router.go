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

	// Parse stack-level env vars
	stackEnvVars := map[string]any{}
	if stack.EnvVars != nil {
		_ = json.Unmarshal([]byte(*stack.EnvVars), &stackEnvVars)
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
			if err := json.Unmarshal([]byte(*c.PortMappings), &pm); err != nil {
				log.Printf("invalid port_mappings JSON for container %s: %v", c.Name, err)
			} else {
				spec["port_mappings"] = pm
			}
		}
		if c.EnvVars != nil {
			var ev map[string]any
			if err := json.Unmarshal([]byte(*c.EnvVars), &ev); err != nil {
				log.Printf("invalid env_vars JSON for container %s: %v", c.Name, err)
			} else {
				// Merge stack-level env vars (container-level overrides stack-level)
				merged := make(map[string]any, len(stackEnvVars)+len(ev))
				for k, v := range stackEnvVars {
					merged[k] = v
				}
				for k, v := range ev {
					merged[k] = v
				}
				spec["env_vars"] = merged
			}
		} else if len(stackEnvVars) > 0 {
			spec["env_vars"] = stackEnvVars
		}
		if c.Volumes != nil {
			var vol map[string]any
			if err := json.Unmarshal([]byte(*c.Volumes), &vol); err != nil {
				log.Printf("invalid volumes JSON for container %s: %v", c.Name, err)
			} else {
				spec["volumes"] = vol
			}
		}
		if c.CPULimit != nil {
			spec["cpu_limit"] = *c.CPULimit
		}
		if c.MemoryLimit != nil {
			spec["memory_limit"] = *c.MemoryLimit
		}
		if c.Command != nil {
			var cmd []string
			if err := json.Unmarshal([]byte(*c.Command), &cmd); err != nil {
				log.Printf("invalid command JSON for container %s: %v", c.Name, err)
			} else {
				spec["command"] = cmd
			}
		}
		if c.Entrypoint != nil {
			var ep []string
			if err := json.Unmarshal([]byte(*c.Entrypoint), &ep); err != nil {
				log.Printf("invalid entrypoint JSON for container %s: %v", c.Name, err)
			} else {
				spec["entrypoint"] = ep
			}
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

	// Record deployment containers
	for _, c := range *containers {
		_, err := query.CreateDeploymentContainer(db.DB, query.CreateDeploymentContainerRequest{
			DeploymentID: deployment.ID,
			ContainerID:  c.ID,
			Image:        c.Image,
			Tag:          c.Tag,
		})
		if err != nil {
			log.Printf("failed to record deployment container %s: %v", c.Name, err)
		}
	}

	deployingStatus := "deploying"
	_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{
		Status: &deployingStatus,
	})

	// Log deployment initiation
	_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deployment initiated by user %d for stack '%s' (strategy=%s, containers=%d)", user.ID, stack.Name, stack.DeploymentStrategy, len(*containers)),
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
		_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
			DeploymentID: deployment.ID,
			Level:        "error",
			Message:      fmt.Sprintf("Failed to send deploy command to worker %d: %v", *stack.WorkerID, err),
		})
		failedStatus := "failed"
		_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{Status: &failedStatus})
		_ = query.UpdateDeploymentStatus(db.DB, deployment.ID, "failed")
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send deploy command: %v", err))
		return
	}

	_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deploy command sent to worker %d via WebSocket", *stack.WorkerID),
	})

	responder.NewCreated(w, deployment, "deployment created and sent to worker")
}
