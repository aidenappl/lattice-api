package routers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
)

const (
	deployPingInterval   = 15 * time.Second
	deployStallTimeout   = 45 * time.Second
	deployMaxRetryCount  = 3
	deployMaxRuntime     = 30 * time.Minute
	maxConcurrentDeploys = 10
)

// deployMonitorSem limits concurrent deployment monitor goroutines.
var deployMonitorSem = make(chan struct{}, maxConcurrentDeploys)

func copyPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	return out
}

func isMonitorGeneratedLog(msg string) bool {
	return strings.HasPrefix(msg, "Runner status check:") ||
		strings.HasPrefix(msg, "No deployment progress detected")
}

// startDeploymentMonitor watches a deployment until it reaches a terminal state.
//
// ctx is the triggering request's context. The monitor outlives the request, so
// only its values are kept: the watchdog's log lines carry the request's ids.
func (h *DeployHandler) startDeploymentMonitor(ctx context.Context, deploymentID, stackID, workerID int, payload map[string]any) {
	ctx = context.WithoutCancel(ctx)
	p := copyPayload(payload)
	go func() {
		select {
		case deployMonitorSem <- struct{}{}:
			defer func() { <-deployMonitorSem }()
			h.monitorDeployment(ctx, deploymentID, stackID, workerID, p)
		default:
			// The full-monitor pool is saturated. Rather than drop this deploy's
			// watchdog entirely (which would leave it able to hang in `deploying`
			// forever), run a lightweight watchdog that does no pinging/retrying but
			// still guarantees the deploy is eventually force-failed if it never
			// reaches a terminal state.
			logger.WarnCtx(ctx, "deploy", "deployment monitor pool saturated, running lightweight watchdog",
				logger.F{"deployment_id": deploymentID, "max": maxConcurrentDeploys})
			h.lightweightDeploymentWatchdog(ctx, deploymentID, stackID)
		}
	}()
}

// lightweightDeploymentWatchdog is the overflow fallback used when the bounded
// monitor pool is full. It polls the deployment status and, if the deploy has
// not reached a terminal state within deployMaxRuntime, force-fails it. It does
// NOT ping the worker or retry the deploy — that heavier work is reserved for
// the bounded monitorDeployment pool — but it ensures no deploy is left without
// a force-fail guarantee.
func (h *DeployHandler) lightweightDeploymentWatchdog(ctx context.Context, deploymentID, stackID int) {
	defer logger.Recover("deployment-watchdog", logger.F{"deployment_id": deploymentID})
	defer forgetRunnerDeploymentStatus(deploymentID)

	ticker := time.NewTicker(deployPingInterval)
	defer ticker.Stop()

	deadline := time.Now().UTC().Add(deployMaxRuntime)

	for range ticker.C {
		dep, err := query.GetDeploymentByID(db.DB, deploymentID)
		if err != nil {
			logger.ErrorCtx(ctx, "deploy", "watchdog failed to load deployment", logger.F{"deployment_id": deploymentID, "error": err})
			continue
		}

		switch dep.Status {
		case "deployed", "failed", "rolled_back":
			return
		}

		if time.Now().UTC().Before(deadline) {
			continue
		}

		writeDeploymentLog(ctx, query.CreateDeploymentLogRequest{
			DeploymentID: deploymentID,
			Level:        "error",
			Message:      fmt.Sprintf("Deployment watchdog timed out after %s with no terminal state", deployMaxRuntime),
		})
		failDeployment(ctx, deploymentID, stackID, "timeout", nil)
		return
	}
}

func (h *DeployHandler) monitorDeployment(ctx context.Context, deploymentID, stackID, workerID int, payload map[string]any) {
	defer logger.Recover("deployment-monitor", logger.F{"deployment_id": deploymentID})
	defer forgetRunnerDeploymentStatus(deploymentID)

	ticker := time.NewTicker(deployPingInterval)
	defer ticker.Stop()

	// Guard against goroutine leak: if the deployment never reaches a terminal
	// state, force-fail it after deployMaxRuntime.
	maxTimer := time.NewTimer(deployMaxRuntime)
	defer maxTimer.Stop()

	attempt := 1
	lastProgressAt := time.Now().UTC()
	pingFailing := false

	for {
		select {
		case <-maxTimer.C:
			writeDeploymentLog(ctx, query.CreateDeploymentLogRequest{
				DeploymentID: deploymentID,
				Level:        "error",
				Message:      fmt.Sprintf("Deployment monitor timed out after %s with no terminal state", deployMaxRuntime),
			})
			failDeployment(ctx, deploymentID, stackID, "timeout", nil)
			return
		case <-ticker.C:
		}
		dep, err := query.GetDeploymentByID(db.DB, deploymentID)
		if err != nil {
			logger.ErrorCtx(ctx, "deploy", "monitor failed to load deployment", logger.F{"deployment_id": deploymentID, "error": err})
			continue
		}

		switch dep.Status {
		case "deployed", "failed", "rolled_back":
			return
		}

		if err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(ctx, socket.MsgDeploymentPing, map[string]any{
			"deployment_id": deploymentID,
		})); err != nil {
			// Not fatal: the stall timeout below decides what happens next. Logged
			// once per outage, not on every tick.
			if !pingFailing {
				logger.WarnCtx(ctx, "deploy", "deployment ping not sent", logger.F{"deployment_id": deploymentID, "worker_id": workerID, "error": err})
			}
			pingFailing = true
		} else {
			pingFailing = false
		}

		latest, err := query.GetLatestDeploymentLog(db.DB, deploymentID)
		if err == nil && latest != nil && !isMonitorGeneratedLog(latest.Message) && latest.RecordedAt.After(lastProgressAt) {
			lastProgressAt = latest.RecordedAt
		}

		if time.Since(lastProgressAt) < deployStallTimeout {
			continue
		}

		if attempt < deployMaxRetryCount {
			attempt++
			retryPayload := copyPayload(payload)
			retryPayload["attempt"] = attempt
			retryPayload["max_retries"] = deployMaxRetryCount
			retryPayload["retry"] = true

			err := h.WorkerHub.SendJSONToWorker(workerID, socket.NewCommand(ctx, socket.MsgDeploy, retryPayload))
			if err != nil {
				writeDeploymentLog(ctx, query.CreateDeploymentLogRequest{
					DeploymentID: deploymentID,
					Level:        "error",
					Message:      fmt.Sprintf("No deployment progress detected; retry %d/%d failed to dispatch: %v", attempt, deployMaxRetryCount, err),
				})
				// Not terminal: the next stall check retries or gives up.
				logger.ErrorCtx(ctx, "deploy", "deployment retry dispatch failed", logger.F{"deployment_id": deploymentID, "worker_id": workerID, "attempt": attempt, "max_retries": deployMaxRetryCount, "error": err})
				continue
			}

			writeDeploymentLog(ctx, query.CreateDeploymentLogRequest{
				DeploymentID: deploymentID,
				Level:        "warning",
				Message:      fmt.Sprintf("No deployment progress detected for %s; retrying deployment (%d/%d)", deployStallTimeout, attempt, deployMaxRetryCount),
			})
			lastProgressAt = time.Now().UTC()
			continue
		}

		writeDeploymentLog(ctx, query.CreateDeploymentLogRequest{
			DeploymentID: deploymentID,
			Level:        "error",
			Message:      fmt.Sprintf("Deployment marked failed after %d stalled attempts with no progress", deployMaxRetryCount),
		})

		failDeployment(ctx, deploymentID, stackID, "stalled", nil, logger.F{"attempts": deployMaxRetryCount})
		return
	}
}
