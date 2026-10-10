package routers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
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

	// Guard: prevent rolling back an already-rolled-back deployment
	if target.Status == "rolled_back" {
		responder.SendError(w, http.StatusConflict, "deployment has already been rolled back")
		return
	}

	stack, err := query.GetStackByID(db.DB, target.StackID)
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

	// Atomically claim the stack for deployment — prevents concurrent rollbacks/deploys.
	// All pre-flight checks passed, so it's safe to transition to "deploying".
	claimed, claimErr := query.ClaimStackForDeploy(db.DB, stack.ID)
	if claimErr != nil {
		responder.QueryError(w, claimErr, "failed to claim stack for rollback")
		return
	}
	if !claimed {
		responder.SendError(w, http.StatusConflict, "deployment already in progress for this stack")
		return
	}

	// The stack is now claimed. Release the claim on every path that does not
	// hand the rollback to the worker or set a terminal status itself, so an
	// early return can't strand the stack in "deploying" for the claim window.
	deploySettled := false
	defer func() {
		if !deploySettled {
			releaseStackClaim(r.Context(), stack.ID)
		}
	}()

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

	// Global env vars as the base layer, the stack's own on top (stack wins).
	// A deploy that cannot read either fails rather than shipping without them.
	mergedEnvVars, envErr := loadDeployEnv(stack.EnvVars)
	if envErr != nil {
		responder.SendError(w, http.StatusInternalServerError, envErr.message, envErr.err)
		return
	}

	// Build container specs: image/tag from the previous deployment, everything
	// else (env vars, ports, volumes, health check …) from the live container record.
	sb := newSpecBuilder(r.Context(), stack.ID, stack.ComposeYAML, mergedEnvVars, true)
	containerSpecs := make([]map[string]any, 0, len(*prevContainers))
	for _, dc := range *prevContainers {
		c, err := query.GetContainerByID(db.DB, dc.ContainerID)
		if err != nil {
			logger.WarnCtx(r.Context(), "deploy", "rollback: container not found, skipping", logger.F{"container_id": dc.ContainerID, "error": err})
			continue
		}

		spec, specErr := sb.build(*c, dc.Image, dc.Tag)
		if specErr != nil {
			responder.SendError(w, http.StatusInternalServerError, specErr.message, specErr.err)
			return
		}
		containerSpecs = append(containerSpecs, spec)
	}

	if len(containerSpecs) == 0 {
		responder.SendError(w, http.StatusBadRequest, "no valid containers found in previous deployment")
		return
	}

	// Create rollback deployment and container records in a transaction.
	tx, txErr := db.BeginTx()
	if txErr != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction", txErr)
		return
	}
	defer tx.Rollback()

	rollbackDeployment, err := query.CreateDeployment(tx, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create rollback deployment")
		return
	}

	for _, dc := range *prevContainers {
		_, err := query.CreateDeploymentContainer(tx, query.CreateDeploymentContainerRequest{
			DeploymentID: rollbackDeployment.ID,
			ContainerID:  dc.ContainerID,
			Image:        dc.Image,
			Tag:          dc.Tag,
		})
		if err != nil {
			responder.QueryError(w, err, fmt.Sprintf("failed to record deployment container %d", dc.ContainerID))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to commit rollback deployment", err)
		return
	}

	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
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

	if err := h.WorkerHub.SendJSONToWorker(*stack.WorkerID, socket.NewCommand(r.Context(), socket.MsgDeploy, payload)); err != nil {
		writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
			DeploymentID: rollbackDeployment.ID,
			Level:        "error",
			Message:      fmt.Sprintf("Failed to send rollback command to worker %d: %v", *stack.WorkerID, err),
		})
		failDeployment(r.Context(), rollbackDeployment.ID, stack.ID, "dispatch_failed", err)
		// failDeployment set a terminal status; don't let the deferred release
		// reset it to active.
		deploySettled = true
		sendDispatchError(w, "rollback", err)
		return
	}

	// Handed off to the worker; the deployment monitor now owns the stack status.
	deploySettled = true

	// Mark the original deployment as rolled back now that the command is dispatched.
	if err := query.UpdateDeploymentStatus(db.DB, targetID, "rolled_back"); err != nil {
		logger.ErrorCtx(r.Context(), "deploy", "could not mark deployment rolled back", logger.F{"deployment_id": targetID, "rollback_deployment_id": rollbackDeployment.ID, "error": err})
	}

	writeDeploymentLog(r.Context(), query.CreateDeploymentLogRequest{
		DeploymentID: rollbackDeployment.ID,
		Level:        "info",
		Message:      fmt.Sprintf("Rollback command sent to worker %d via WebSocket", *stack.WorkerID),
	})

	h.startDeploymentMonitor(r.Context(), rollbackDeployment.ID, stack.ID, *stack.WorkerID, payload)

	logAudit(r, "rollback", "deployment", intPtr(targetID), nil)
	responder.NewCreated(w, rollbackDeployment, "rollback deployment created and sent to worker")
}
