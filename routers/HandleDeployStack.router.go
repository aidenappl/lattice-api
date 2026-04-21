package routers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

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

	if stack.Status == "deploying" {
		responder.SendError(w, http.StatusConflict, "deployment already in progress for this stack")
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

	// Parse stack-level env vars
	stackEnvVars := map[string]any{}
	if stack.EnvVars != nil {
		_ = json.Unmarshal([]byte(*stack.EnvVars), &stackEnvVars)
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
				log.Printf("invalid port_mappings JSON for container %s: %v", c.Name, err)
			} else {
				// Resolve environment variable references in port mappings
				resolved := resolveVarsInValue(pm, stackEnvVars)
				spec["port_mappings"] = resolved
			}
		}
		if c.EnvVars != nil {
			var ev map[string]any
			if err := json.Unmarshal([]byte(*c.EnvVars), &ev); err != nil {
				log.Printf("invalid env_vars JSON for container %s: %v", c.Name, err)
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
				log.Printf("invalid volumes JSON for container %s: %v", c.Name, err)
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
			spec["memory_limit"] = int64(*c.MemoryLimit) * 1024 * 1024 // convert MB to bytes for Docker
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
		if c.HealthCheck != nil {
			var hc map[string]any
			if err := json.Unmarshal([]byte(*c.HealthCheck), &hc); err != nil {
				log.Printf("invalid health_check JSON for container %s: %v", c.Name, err)
			} else {
				// Resolve env var references in health check against both stack-level
				// and container-level env vars (container vars take precedence).
				allEnvVars := make(map[string]any, len(stackEnvVars))
				for k, v := range stackEnvVars {
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
						log.Printf("deploy: auto-matched registry %q for image %s", reg.Name, c.Image)
					}
					break
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
