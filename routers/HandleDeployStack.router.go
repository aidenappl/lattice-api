package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
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

	// Optional body — if container_ids is provided, only deploy those containers.
	// If force is true, all existing stack containers are removed before deploying.
	var body struct {
		ContainerIDs []int `json:"container_ids"`
		Force        bool  `json:"force"`
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

	// The stack is now claimed (status=deploying, deploy_claimed_at=NOW()).
	// Guarantee the claim is released on EVERY post-claim failure path: if we
	// don't successfully hand the deploy off to the worker (or explicitly set a
	// terminal status ourselves), reset the stack to active so a transient DB
	// error can't strand it in "deploying" for the full 30-minute claim window.
	deploySettled := false
	defer func() {
		if !deploySettled {
			releaseStackClaim(r.Context(), stack.ID)
		}
	}()

	// Fetch containers for this stack
	containers, err := query.ListContainersByStack(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	// If specific container IDs were requested, filter to only those
	targeted := len(body.ContainerIDs) > 0
	if targeted && body.Force {
		// deferred unclaim resets the stack to active
		responder.SendError(w, http.StatusBadRequest, "force deploy cannot be combined with targeted container deployment")
		return
	}
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
			// deferred unclaim resets the stack to active
			responder.SendError(w, http.StatusBadRequest, "none of the specified container IDs belong to this stack")
			return
		}
		containers = &filtered
	}

	// Global env vars as the base layer, the stack's own on top (stack wins).
	// A deploy that cannot read either fails rather than shipping without them.
	mergedEnvVars, envErr := loadDeployEnv(stack.EnvVars)
	if envErr != nil {
		responder.SendError(w, http.StatusInternalServerError, envErr.message, envErr.err)
		return
	}

	// Build container specs with registry auth resolved
	sb := newSpecBuilder(r.Context(), stack.ID, stack.ComposeYAML, mergedEnvVars, false)
	containerSpecs := make([]map[string]any, 0, len(*containers))
	for _, c := range *containers {
		spec, specErr := sb.build(c, c.Image, c.Tag)
		if specErr != nil {
			responder.SendError(w, http.StatusInternalServerError, specErr.message, specErr.err)
			return
		}
		containerSpecs = append(containerSpecs, spec)
	}

	// Create deployment record and container records in a transaction so
	// partial failures don't leave orphaned state.
	tx, txErr := db.BeginTx()
	if txErr != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction", txErr)
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
		responder.SendError(w, http.StatusInternalServerError, "failed to commit deployment", err)
		return
	}

	// Log deployment initiation
	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deployment initiated by user %d for stack '%s' (strategy=%s, containers=%d, targeted=%v, force=%v)", user.ID, stack.Name, stack.DeploymentStrategy, len(*containers), targeted, body.Force),
	})

	// Send deploy command to worker
	payload := map[string]any{
		"deployment_id": deployment.ID,
		"stack_name":    stack.Name,
		"strategy":      stack.DeploymentStrategy,
		"targeted":      targeted,
		"force":         body.Force,
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
		// We've set an explicit terminal (failed) status — don't let the deferred
		// unclaim reset it back to active.
		deploySettled = true
		sendDispatchError(w, "deploy", err)
		return
	}

	// Deploy command handed off to the worker; the deployment monitor now owns
	// the stack status. Suppress the deferred unclaim.
	deploySettled = true

	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
		DeploymentID: deployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Deploy command sent to worker %d via WebSocket", *stack.WorkerID),
	})

	h.startDeploymentMonitor(r.Context(), deployment.ID, stack.ID, *stack.WorkerID, payload)

	triggeredBy := user.Email
	if user.Name != nil {
		triggeredBy = *user.Name
	}
	mailer.Notify("deployment.triggered", "Deployment Triggered",
		fmt.Sprintf("Stack <strong>%s</strong> deployment triggered by <strong>%s</strong>.\n\nStrategy: %s\nContainers: %d",
			stack.Name, triggeredBy, stack.DeploymentStrategy, len(*containers)))

	deployAuditDetails := stack.Name
	if targeted {
		names := make([]string, 0, len(*containers))
		for _, c := range *containers {
			names = append(names, c.Name)
		}
		deployAuditDetails += fmt.Sprintf(" [%s]", strings.Join(names, ", "))
	}
	logAudit(r, "deploy", "stack", intPtr(stackID), strPtr(deployAuditDetails))
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
