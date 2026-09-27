package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

func (h *DeployHandler) HandlePublicDeploy(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]
	if token == "" {
		responder.SendError(w, http.StatusUnauthorized, "missing deploy token")
		return
	}

	hash := tools.HashToken(token)
	dt, err := query.GetDeployTokenByHash(db.DB, hash)
	if err != nil || dt == nil || !dt.Active {
		responder.SendError(w, http.StatusUnauthorized, "invalid deploy token")
		return
	}

	if err := query.TouchDeployToken(db.DB, dt.ID); err != nil {
		// The deploy proceeds; the token's last-used time is what goes stale.
		logger.WarnCtx(r.Context(), "deploy", "could not record deploy token use", logger.F{"deploy_token_id": dt.ID, "stack_id": dt.StackID, "error": err})
	}

	// Look up the stack
	stack, err := query.GetStackByID(db.DB, dt.StackID)
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

	// If ?container=name is specified, only recreate that single container
	// instead of doing a full stack deployment.
	if containerName := r.URL.Query().Get("container"); containerName != "" {
		h.handleSingleContainerDeploy(w, r, stack, containerName, dt)
		return
	}

	// Atomically claim the stack for deployment — prevents the check-then-set
	// TOCTOU race where two concurrent token deploys both pass a status check
	// and proceed. A second concurrent deploy/rollback returns 409.
	claimed, err := query.ClaimStackForDeploy(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to claim stack for deploy")
		return
	}
	if !claimed {
		responder.SendError(w, http.StatusConflict, "deployment already in progress for this stack")
		return
	}

	// Guarantee the claim is released on every post-claim failure path so a
	// transient error can't strand the stack in "deploying" for 30 minutes.
	deploySettled := false
	defer func() {
		if !deploySettled {
			releaseStackClaim(r.Context(), stack.ID)
		}
	}()

	// Validate placement constraints against worker labels
	if stack.PlacementConstraints != nil && *stack.PlacementConstraints != "" {
		worker, wErr := query.GetWorkerByID(db.DB, *stack.WorkerID)
		if wErr == nil && worker != nil {
			var constraints map[string]string
			if json.Unmarshal([]byte(*stack.PlacementConstraints), &constraints) == nil {
				workerLabels := parseWorkerLabels(worker.Labels)
				for key, value := range constraints {
					if workerLabels[key] != value {
						responder.SendError(w, http.StatusBadRequest,
							fmt.Sprintf("worker does not satisfy placement constraint: %s=%s", key, value))
						return
					}
				}
			}
		}
	}

	// Fetch containers for this stack
	containers, err := query.ListContainersByStack(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	// Global env vars as the base layer, the stack's own on top (stack wins).
	// A deploy that cannot read either fails rather than shipping without them.
	mergedEnvVars, envErr := loadDeployEnv(stack.EnvVars)
	if envErr != nil {
		responder.SendError(w, http.StatusInternalServerError, envErr.message, envErr.err)
		return
	}

	// Load all registries for auto-matching by image hostname
	allRegistries, _ := query.ListRegistries(db.DB)

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
				logger.ErrorCtx(r.Context(), "deploy", "invalid port_mappings JSON", logger.F{"container": c.Name, "error": err})
			} else {
				resolved := resolveVarsInValue(pm, mergedEnvVars)
				spec["port_mappings"] = resolved
			}
		}
		ev, err := parseContainerEnvVars(c.EnvVars)
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "container env vars could not be read", fmt.Errorf("container %s: %w", c.Name, err))
			return
		}
		if ev != nil {
			merged := make(map[string]any, len(ev))
			for k, v := range ev {
				if s, ok := v.(string); ok {
					if resolved, ok := resolveEnvRef(s, mergedEnvVars); ok {
						merged[k] = resolved
						continue
					}
				}
				merged[k] = v
			}
			spec["env_vars"] = merged
		}
		if c.Volumes != nil {
			var vol map[string]any
			if err := json.Unmarshal([]byte(*c.Volumes), &vol); err != nil {
				logger.ErrorCtx(r.Context(), "deploy", "invalid volumes JSON", logger.F{"container": c.Name, "error": err})
			} else {
				resolved := resolveVarsInValue(vol, mergedEnvVars)
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
				logger.ErrorCtx(r.Context(), "deploy", "invalid command JSON", logger.F{"container": c.Name, "error": err})
			} else {
				spec["command"] = cmd
			}
		}
		if c.Entrypoint != nil {
			var ep []string
			if err := json.Unmarshal([]byte(*c.Entrypoint), &ep); err != nil {
				logger.ErrorCtx(r.Context(), "deploy", "invalid entrypoint JSON", logger.F{"container": c.Name, "error": err})
			} else {
				spec["entrypoint"] = ep
			}
		}
		if c.HealthCheck != nil {
			var hc map[string]any
			if err := json.Unmarshal([]byte(*c.HealthCheck), &hc); err != nil {
				logger.ErrorCtx(r.Context(), "deploy", "invalid health_check JSON", logger.F{"container": c.Name, "error": err})
			} else {
				allEnvVars := make(map[string]any, len(mergedEnvVars))
				for k, v := range mergedEnvVars {
					allEnvVars[k] = v
				}
				if containerEnvs, ok := spec["env_vars"].(map[string]any); ok {
					for k, v := range containerEnvs {
						allEnvVars[k] = v
					}
				}
				resolved := resolveVarsInValue(hc, allEnvVars)
				spec["health_check"] = resolved
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
		} else if allRegistries != nil {
			for _, reg := range *allRegistries {
				regHost := strings.TrimPrefix(strings.TrimPrefix(reg.URL, "https://"), "http://")
				regHost = strings.TrimSuffix(regHost, "/")
				if strings.HasPrefix(c.Image, regHost+"/") || c.Image == regHost {
					auth := map[string]string{}
					if reg.Username != nil {
						auth["username"] = *reg.Username
					}
					if reg.Password != nil {
						auth["password"] = *reg.Password
					}
					if len(auth) > 0 {
						spec["registry_auth"] = auth
						logger.InfoCtx(r.Context(), "deploy", "auto-matched registry", logger.F{"registry": reg.Name, "image": c.Image})
					}
					break
				}
			}
		}

		containerSpecs = append(containerSpecs, spec)
	}

	// Create deployment record (no user — triggered by CI/CD token)
	deployment, err := query.CreateDeployment(db.DB, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: nil,
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
			logger.ErrorCtx(r.Context(), "deploy", "failed to record deployment container", logger.F{"container": c.Name, "error": err})
		}
	}

	// Stack is already in "deploying" from ClaimStackForDeploy above.

	// Log deployment initiation
	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deployment initiated by deploy token %q for stack '%s' (strategy=%s, containers=%d)", dt.Name, stack.Name, stack.DeploymentStrategy, len(*containers)),
	})

	// Send deploy command to worker
	payload := map[string]any{
		"deployment_id": deployment.ID,
		"stack_name":    stack.Name,
		"strategy":      stack.DeploymentStrategy,
		"containers":    containerSpecs,
		"attempt":       1,
		"max_retries":   deployMaxRetryCount,
	}

	// Include stack-level networks
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

	// Include stack-level volumes
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

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.NewCommand(r.Context(), socket.MsgDeploy, payload)); err != nil {
		writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
			DeploymentID: deployment.ID,
			Level:        "error",
			Message:      fmt.Sprintf("Failed to send deploy command to worker %d: %v", *stack.WorkerID, err),
		})
		failDeployment(r.Context(), deployment.ID, stack.ID, "dispatch_failed", err)
		// Explicit terminal status set — suppress the deferred unclaim.
		deploySettled = true
		sendDispatchError(w, "deploy", err)
		return
	}

	// Deploy handed off to the worker; the monitor now owns the stack status.
	deploySettled = true

	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deploy command sent to worker %d via WebSocket", *stack.WorkerID),
	})

	h.startDeploymentMonitor(r.Context(), deployment.ID, stack.ID, *stack.WorkerID, payload)

	mailer.Notify("deployment.triggered", "Deployment Triggered",
		fmt.Sprintf("Stack <strong>%s</strong> deployment triggered via deploy token <strong>%s</strong>.\n\nStrategy: %s\nContainers: %d",
			stack.Name, dt.Name, stack.DeploymentStrategy, len(*containers)))

	auditDetails := fmt.Sprintf("%s via deploy token %q", stack.Name, dt.Name)
	if commit := r.URL.Query().Get("commit"); commit != "" {
		auditDetails += fmt.Sprintf(" @ %s", commit)
	}
	logAudit(r, "deploy", "stack", intPtr(stack.ID), strPtr(auditDetails))

	responder.NewCreated(w, deployment, "deployment created and sent to worker")
}

// handleSingleContainerDeploy recreates a single container by name within a stack.
// Used when the deploy token URL includes ?container=name.
func (h *DeployHandler) handleSingleContainerDeploy(w http.ResponseWriter, r *http.Request, stack *structs.Stack, containerName string, dt *structs.DeployToken) {
	// Find the container in this stack
	containers, err := query.ListContainersByStack(db.DB, stack.ID)
	if err == nil && containers == nil {
		err = fmt.Errorf("no container list for stack %d", stack.ID)
	}
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to list containers", err)
		return
	}

	var target *structs.Container
	for _, c := range *containers {
		if c.Name == containerName {
			target = &c
			break
		}
	}
	if target == nil {
		responder.SendError(w, http.StatusNotFound, fmt.Sprintf("container %q not found in stack %q", containerName, stack.Name))
		return
	}

	// Same payload as HandleRecreateContainer and automations — one builder.
	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.NewCommand(r.Context(), socket.MsgRecreate, recreateContainerPayload(target))); err != nil {
		sendDispatchError(w, "recreate", err)
		return
	}

	logger.InfoCtx(r.Context(), "deploy", "single container deploy triggered via token", logger.F{
		"stack":     stack.Name,
		"container": containerName,
		"token":     dt.Name,
	})

	mailer.Notify("deployment.triggered", "Deployment Triggered",
		fmt.Sprintf("Container <strong>%s</strong> in stack <strong>%s</strong> recreated via deploy token <strong>%s</strong>.",
			containerName, stack.Name, dt.Name))

	containerAuditDetails := fmt.Sprintf("%s/%s via deploy token %q", stack.Name, containerName, dt.Name)
	if commit := r.URL.Query().Get("commit"); commit != "" {
		containerAuditDetails += fmt.Sprintf(" @ %s", commit)
	}
	logAudit(r, "deploy_container", "container", intPtr(target.ID), strPtr(containerAuditDetails))

	responder.New(w, map[string]any{
		"container": containerName,
		"action":    "recreate",
	}, fmt.Sprintf("recreate command sent to %s", containerName))
}
