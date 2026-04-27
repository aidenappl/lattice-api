package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"
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

	// Optional body — if container_ids is provided, only deploy those containers.
	var body struct {
		ContainerIDs []int `json:"container_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

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

	// Pre-flight validation — must pass before we claim the stack, otherwise
	// a failed check leaves the stack stuck in "deploying" with no deployment.
	if stack.WorkerID == nil {
		responder.SendError(w, http.StatusBadRequest, "stack has no worker assigned")
		return
	}

	if !h.WorkerHub.IsConnected(*stack.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

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

	// Atomically claim the stack for deployment — prevents concurrent deploys.
	// All pre-flight checks passed, so it's safe to transition to "deploying".
	claimed, err := query.ClaimStackForDeploy(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to claim stack for deploy")
		return
	}
	if !claimed {
		responder.SendError(w, http.StatusConflict, "deployment already in progress for this stack")
		return
	}

	// Fetch containers for this stack
	containers, err := query.ListContainersByStack(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	// If specific container IDs were requested, filter to only those
	targeted := len(body.ContainerIDs) > 0
	if targeted {
		idSet := make(map[int]bool, len(body.ContainerIDs))
		for _, cid := range body.ContainerIDs {
			idSet[cid] = true
		}
		filtered := make([]structs.Container, 0, len(body.ContainerIDs))
		for _, c := range *containers {
			if idSet[c.ID] {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			failedStatus := "active"
			_, _ = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{Status: &failedStatus})
			responder.SendError(w, http.StatusBadRequest, "none of the specified container IDs belong to this stack")
			return
		}
		containers = &filtered
	}

	// Fetch stack-level networks — assigned to every container so Docker DNS works
	stackNetworks, _ := query.ListNetworksByStack(db.DB, stack.ID)
	var networkNames []string
	if stackNetworks != nil {
		for _, n := range *stackNetworks {
			networkNames = append(networkNames, n.Name)
		}
	}

	// Load global env vars and merge as base layer
	globalVars, _ := query.ListGlobalEnvVars(db.DB)
	globalEnvMap := make(map[string]any)
	if globalVars != nil {
		for _, gv := range *globalVars {
			decrypted, _ := crypto.Decrypt(gv.EncryptedValue)
			globalEnvMap[gv.Key] = decrypted
		}
	}

	// Parse stack-level env vars
	stackEnvVars := map[string]any{}
	if stack.EnvVars != nil {
		_ = json.Unmarshal([]byte(*stack.EnvVars), &stackEnvVars)
	}

	// Merge: global -> stack (stack wins)
	mergedEnvVars := make(map[string]any)
	for k, v := range globalEnvMap {
		mergedEnvVars[k] = v
	}
	for k, v := range stackEnvVars {
		mergedEnvVars[k] = v
	}

	// Load all registries for auto-matching by image hostname
	allRegistries, _ := query.ListRegistries(db.DB)

	// Build a compose-derived alias map so containers created before the
	// network_aliases column was added still get the correct DNS aliases.
	// Maps container name -> []string of compose service keys.
	composeAliases := make(map[string][]string)
	if stack.ComposeYAML != nil && *stack.ComposeYAML != "" {
		var compose composeFile
		if yaml.Unmarshal([]byte(*stack.ComposeYAML), &compose) == nil {
			for svcKey, svc := range compose.Services {
				cName := svcKey
				if svc.ContainerName != "" {
					cName = svc.ContainerName
				}
				// If the container name differs from the service key, the service
				// key needs to be a DNS alias (docker-compose does this automatically).
				if cName != svcKey {
					composeAliases[cName] = append(composeAliases[cName], svcKey)
				}
			}
		}
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

		// Assign all stack-level networks to every container so Docker DNS resolves
		if len(networkNames) > 0 {
			spec["networks"] = networkNames
		}

		// Network aliases — merge from DB column and compose-derived map.
		// This ensures containers get their compose service names as DNS aliases
		// even if they were imported before the network_aliases column existed.
		var aliases []string
		if c.NetworkAliases != nil {
			_ = json.Unmarshal([]byte(*c.NetworkAliases), &aliases)
		}
		if composeAlias, ok := composeAliases[c.Name]; ok {
			for _, a := range composeAlias {
				found := false
				for _, existing := range aliases {
					if existing == a {
						found = true
						break
					}
				}
				if !found {
					aliases = append(aliases, a)
				}
			}
		}
		if len(aliases) > 0 {
			spec["network_aliases"] = aliases
		}

		if c.PortMappings != nil {
			var pm []any
			if err := json.Unmarshal([]byte(*c.PortMappings), &pm); err != nil {
				logger.Error("deploy", "invalid port_mappings JSON", logger.F{"container": c.Name, "error": err})
			} else {
				// Resolve environment variable references in port mappings
				resolved := resolveVarsInValue(pm, mergedEnvVars)
				spec["port_mappings"] = resolved
			}
		}
		if c.EnvVars != nil {
			var ev map[string]any
			if err := json.Unmarshal([]byte(*c.EnvVars), &ev); err != nil {
				logger.Error("deploy", "invalid env_vars JSON", logger.F{"container": c.Name, "error": err})
			} else {
				// Preserve compose semantics: only include env keys explicitly defined
				// for the service, but resolve ${VAR} references from stack-level env.
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
		}
		if c.Volumes != nil {
			var vol map[string]any
			if err := json.Unmarshal([]byte(*c.Volumes), &vol); err != nil {
				logger.Error("deploy", "invalid volumes JSON", logger.F{"container": c.Name, "error": err})
			} else {
				// Resolve environment variable references in volumes
				resolved := resolveVarsInValue(vol, mergedEnvVars)
				spec["volumes"] = resolved
			}
		}
		if c.CPULimit != nil {
			spec["cpu_limit"] = *c.CPULimit
		}
		if c.MemoryLimit != nil {
			spec["memory_limit"] = int64(*c.MemoryLimit) * 1024 * 1024 // convert MB to bytes for Docker
		}
		if c.Command != nil {
			var cmd []string
			if err := json.Unmarshal([]byte(*c.Command), &cmd); err != nil {
				logger.Error("deploy", "invalid command JSON", logger.F{"container": c.Name, "error": err})
			} else {
				spec["command"] = cmd
			}
		}
		if c.Entrypoint != nil {
			var ep []string
			if err := json.Unmarshal([]byte(*c.Entrypoint), &ep); err != nil {
				logger.Error("deploy", "invalid entrypoint JSON", logger.F{"container": c.Name, "error": err})
			} else {
				spec["entrypoint"] = ep
			}
		}
		if c.HealthCheck != nil {
			var hc map[string]any
			if err := json.Unmarshal([]byte(*c.HealthCheck), &hc); err != nil {
				logger.Error("deploy", "invalid health_check JSON", logger.F{"container": c.Name, "error": err})
			} else {
				// Resolve env var references in health check against both stack-level
				// and container-level env vars (container vars take precedence).
				allEnvVars := make(map[string]any, len(mergedEnvVars))
				for k, v := range mergedEnvVars {
					allEnvVars[k] = v
				}
				// Merge container-level env vars on top
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
			// Auto-match registry by image hostname
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
						logger.Info("deploy", "auto-matched registry", logger.F{"registry": reg.Name, "image": c.Image})
					}
					break
				}
			}
		}

		containerSpecs = append(containerSpecs, spec)
	}

	// Create deployment record and container records in a transaction so
	// partial failures don't leave orphaned state.
	tx, txErr := db.BeginTx()
	if txErr != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback()

	deployment, err := query.CreateDeployment(tx, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create deployment")
		return
	}

	for _, c := range *containers {
		_, err := query.CreateDeploymentContainer(tx, query.CreateDeploymentContainerRequest{
			DeploymentID: deployment.ID,
			ContainerID:  c.ID,
			Image:        c.Image,
			Tag:          c.Tag,
		})
		if err != nil {
			responder.QueryError(w, err, fmt.Sprintf("failed to record deployment container %s", c.Name))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to commit deployment")
		return
	}

	// Log deployment initiation
	_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deployment initiated by user %d for stack '%s' (strategy=%s, containers=%d, targeted=%v)", user.ID, stack.Name, stack.DeploymentStrategy, len(*containers), targeted),
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

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.Envelope{
		Type:    socket.MsgDeploy,
		Payload: payload,
	}); err != nil {
		logger.Error("deploy", "failed to send deploy command to worker", logger.F{"worker_id": *stack.WorkerID, "error": err})
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

	h.startDeploymentMonitor(deployment.ID, stack.ID, *stack.WorkerID, payload)

	logAudit(r, "deploy", "stack", intPtr(stackID), strPtr(stack.Name))
	responder.NewCreated(w, deployment, "deployment created and sent to worker")
}

// resolveEnvRef checks if s is a compose-style variable reference (${VAR} or $VAR)
// and, if so, returns the corresponding value from envVars. Returns ("", false) if
// s is not a reference or the variable is not present in envVars.
func resolveEnvRef(s string, envVars map[string]any) (any, bool) {
	var name string
	switch {
	case strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"):
		name = s[2 : len(s)-1]
	case strings.HasPrefix(s, "$"):
		name = s[1:]
	default:
		return nil, false
	}
	if name == "" {
		return nil, false
	}
	if v, ok := envVars[name]; ok {
		return v, true
	}
	return nil, false
}

var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolateEnvRefs replaces ${VAR} and $VAR occurrences inside s using
// stack-level env vars. Unknown variables are left unchanged.
func interpolateEnvRefs(s string, envVars map[string]any) string {
	return envRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		if resolved, ok := resolveEnvRef(match, envVars); ok {
			return fmt.Sprint(resolved)
		}
		return match
	})
}

// resolveVarsInValue recursively resolves environment variable references in any value.
// Handles strings (${VAR} syntax), maps, and slices.
func resolveVarsInValue(val any, envVars map[string]any) any {
	switch v := val.(type) {
	case string:
		// Try to resolve as an environment variable reference
		if resolved, ok := resolveEnvRef(v, envVars); ok {
			return resolved
		}
		// Resolve embedded references in larger strings, e.g.
		// "http://localhost:${PORT}/healthcheck".
		return interpolateEnvRefs(v, envVars)
	case map[string]any:
		// Recursively resolve all values in the map
		result := make(map[string]any, len(v))
		for k, value := range v {
			result[k] = resolveVarsInValue(value, envVars)
		}
		return result
	case []any:
		// Recursively resolve all elements in the slice
		result := make([]any, len(v))
		for i, value := range v {
			result[i] = resolveVarsInValue(value, envVars)
		}
		return result
	default:
		// For other types (int, float, bool, nil), return as-is
		return val
	}
}
