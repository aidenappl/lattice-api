package routers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

func (h *DeployHandler) HandleRollbackDeployment(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}

	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Fetch the deployment being rolled back.
	target, err := query.GetDeploymentByID(db.DB, targetID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	stack, err := query.GetStackByID(db.DB, target.StackID)
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

	// Find the most recent successfully-deployed deployment before the target.
	prev, err := query.GetPreviousDeployment(db.DB, target.StackID, targetID)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "no previous successful deployment found to rollback to")
		return
	}

	// Load the containers from the previous deployment (carries image+tag).
	prevContainers, err := query.ListDeploymentContainers(db.DB, prev.ID)
	if err != nil || prevContainers == nil || len(*prevContainers) == 0 {
		responder.SendError(w, http.StatusBadRequest, "previous deployment has no container records")
		return
	}

	// Stack-level env vars (same as deploy path).
	stackEnvVars := map[string]any{}
	if stack.EnvVars != nil {
		_ = json.Unmarshal([]byte(*stack.EnvVars), &stackEnvVars)
	}

	allRegistries, _ := query.ListRegistries(db.DB)

	// Build container specs: image/tag from the previous deployment, everything
	// else (env vars, ports, volumes, health check …) from the live container record.
	containerSpecs := make([]map[string]any, 0, len(*prevContainers))
	for _, dc := range *prevContainers {
		c, err := query.GetContainerByID(db.DB, dc.ContainerID)
		if err != nil {
			log.Printf("rollback: container %d not found, skipping: %v", dc.ContainerID, err)
			continue
		}

		spec := map[string]any{
			"id":             c.ID,
			"name":           c.Name,
			"image":          dc.Image,
			"tag":            dc.Tag,
			"replicas":       c.Replicas,
			"restart_policy": c.RestartPolicy,
		}

		if c.PortMappings != nil {
			var pm []any
			if err := json.Unmarshal([]byte(*c.PortMappings), &pm); err != nil {
				log.Printf("rollback: invalid port_mappings JSON for container %s: %v", c.Name, err)
			} else {
				// Resolve environment variable references in port mappings
				resolved := resolveVarsInValue(pm, stackEnvVars)
				spec["port_mappings"] = resolved
			}
		}

		if c.EnvVars != nil {
			var ev map[string]any
			if err := json.Unmarshal([]byte(*c.EnvVars), &ev); err != nil {
				log.Printf("rollback: invalid env_vars JSON for container %s: %v", c.Name, err)
			} else {
				// Preserve compose semantics: only include env keys explicitly defined
				// for the service, but resolve ${VAR} references from stack-level env.
				merged := make(map[string]any, len(ev))
				for k, v := range ev {
					if s, ok := v.(string); ok {
						if resolved, ok := resolveEnvRef(s, stackEnvVars); ok {
							merged[k] = resolved
							continue
						}
					}
					merged[k] = v
				}
				spec["env_vars"] = merged
			}
		}

		if c.Volumes != nil {
			var vol map[string]any
			if err := json.Unmarshal([]byte(*c.Volumes), &vol); err != nil {
				log.Printf("rollback: invalid volumes JSON for container %s: %v", c.Name, err)
			} else {
				// Resolve environment variable references in volumes
				resolved := resolveVarsInValue(vol, stackEnvVars)
				spec["volumes"] = resolved
			}
		}

		if c.CPULimit != nil {
			spec["cpu_limit"] = *c.CPULimit
		}
		if c.MemoryLimit != nil {
			spec["memory_limit"] = int64(*c.MemoryLimit) * 1024 * 1024
		}

		if c.Command != nil {
			var cmd []string
			if err := json.Unmarshal([]byte(*c.Command), &cmd); err != nil {
				log.Printf("rollback: invalid command JSON for container %s: %v", c.Name, err)
			} else {
				spec["command"] = cmd
			}
		}

		if c.Entrypoint != nil {
			var ep []string
			if err := json.Unmarshal([]byte(*c.Entrypoint), &ep); err != nil {
				log.Printf("rollback: invalid entrypoint JSON for container %s: %v", c.Name, err)
			} else {
				spec["entrypoint"] = ep
			}
		}

		if c.HealthCheck != nil {
			var hc map[string]any
			if err := json.Unmarshal([]byte(*c.HealthCheck), &hc); err != nil {
				log.Printf("rollback: invalid health_check JSON for container %s: %v", c.Name, err)
			} else {
				spec["health_check"] = hc
			}
		}

		// Resolve registry credentials.
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
		} else if allRegistries != nil {
			for _, reg := range *allRegistries {
				regHost := strings.TrimPrefix(strings.TrimPrefix(reg.URL, "https://"), "http://")
				regHost = strings.TrimSuffix(regHost, "/")
				if strings.HasPrefix(dc.Image, regHost+"/") || dc.Image == regHost {
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
					break
				}
			}
		}

		containerSpecs = append(containerSpecs, spec)
	}

	if len(containerSpecs) == 0 {
		responder.SendError(w, http.StatusBadRequest, "no valid containers found in previous deployment")
		return
	}

	// Create a new deployment record for this rollback.
	rollbackDeployment, err := query.CreateDeployment(db.DB, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create rollback deployment")
		return
	}

	// Record the containers being rolled back to.
	for _, dc := range *prevContainers {
		_, err := query.CreateDeploymentContainer(db.DB, query.CreateDeploymentContainerRequest{
			DeploymentID: rollbackDeployment.ID,
			ContainerID:  dc.ContainerID,
			Image:        dc.Image,
			Tag:          dc.Tag,
		})
		if err != nil {
			log.Printf("rollback: failed to record deployment container %d: %v", dc.ContainerID, err)
		}
	}

	deployingStatus := "deploying"
	_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{Status: &deployingStatus})

	_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: rollbackDeployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Rollback initiated by user %d for stack '%s': reverting deployment %d → %d (%d containers)", user.ID, stack.Name, targetID, prev.ID, len(containerSpecs)),
	})

	payload := map[string]any{
		"deployment_id": rollbackDeployment.ID,
		"stack_name":    stack.Name,
		"strategy":      stack.DeploymentStrategy,
		"containers":    containerSpecs,
		"rollback":      true,
		"rollback_of":   targetID,
		"attempt":       1,
		"max_retries":   deployMaxRetryCount,
	}

	if networks, err := query.ListNetworksByStack(db.DB, stack.ID); err == nil && networks != nil && len(*networks) > 0 {
		netSpecs := make([]map[string]any, 0, len(*networks))
		for _, n := range *networks {
			netSpecs = append(netSpecs, map[string]any{
				"name":   n.Name,
				"driver": n.Driver,
			})
		}
		payload["networks"] = netSpecs
	}

	if volumes, err := query.ListVolumesByStack(db.DB, stack.ID); err == nil && volumes != nil && len(*volumes) > 0 {
		volSpecs := make([]map[string]any, 0, len(*volumes))
		for _, v := range *volumes {
			volSpecs = append(volSpecs, map[string]any{
				"name":   v.Name,
				"driver": v.Driver,
			})
		}
		payload["volumes"] = volSpecs
	}

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
		Type:    socket.MsgDeploy,
		Payload: payload,
	}); err != nil {
		log.Printf("rollback: failed to send to worker=%d: %v", *stack.WorkerID, err)
		_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
			DeploymentID: rollbackDeployment.ID,
			Level:        "error",
			Message:      fmt.Sprintf("Failed to send rollback command to worker %d: %v", *stack.WorkerID, err),
		})
		failedStatus := "failed"
		_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{Status: &failedStatus})
		_ = query.UpdateDeploymentStatus(db.DB, rollbackDeployment.ID, "failed")
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send rollback command: %v", err))
		return
	}

	// Mark the original deployment as rolled back now that the command is dispatched.
	_ = query.UpdateDeploymentStatus(db.DB, targetID, "rolled_back")

	_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: rollbackDeployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Rollback command sent to worker %d via WebSocket", *stack.WorkerID),
	})

	h.startDeploymentMonitor(rollbackDeployment.ID, stack.ID, *stack.WorkerID, payload)

	logAudit(r, "rollback", "deployment", intPtr(targetID), nil)
	responder.NewCreated(w, rollbackDeployment, "rollback deployment created and sent to worker")
}
