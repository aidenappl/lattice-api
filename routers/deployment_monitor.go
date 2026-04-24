package routers

import (
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
)

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

func (h *DeployHandler) startDeploymentMonitor(deploymentID, stackID, workerID int, payload map[string]any) {
	go h.monitorDeployment(deploymentID, stackID, workerID, copyPayload(payload))
}

func (h *DeployHandler) monitorDeployment(deploymentID, stackID, workerID int, payload map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("panic", fmt.Sprintf("%v", r), logger.F{"goroutine": "deployment-monitor", "deployment_id": deploymentID})
		}
	}()

	ticker := time.NewTicker(deployPingInterval)
	defer ticker.Stop()

	// Guard against goroutine leak: if the deployment never reaches a terminal
	// state, force-fail it after deployMaxRuntime.
	maxTimer := time.NewTimer(deployMaxRuntime)
	defer maxTimer.Stop()

	attempt := 1
	lastProgressAt := time.Now().UTC()

	for {
		select {
		case <-maxTimer.C:
			logger.Warn("deploy", "deployment monitor exceeded maximum runtime, marking as failed",
				logger.F{"deployment_id": deploymentID, "max_runtime": deployMaxRuntime.String()})
			_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
				DeploymentID: deploymentID,
				Level:        "error",
				Message:      fmt.Sprintf("Deployment monitor timed out after %s with no terminal state", deployMaxRuntime),
			})
			failedStatus := "failed"
			_, _ = query.UpdateStack(db.DB, stackID, query.UpdateStackRequest{Status: &failedStatus})
			_ = query.UpdateDeploymentStatus(db.DB, deploymentID, "failed")
			return
		case <-ticker.C:
		}
		dep, err := query.GetDeploymentByID(db.DB, deploymentID)
		if err != nil {
			logger.Error("deploy", "monitor failed to load deployment", logger.F{"deployment_id": deploymentID, "error": err})
			continue
		}

		switch dep.Status {
		case "deployed", "failed", "rolled_back":
			return
		}

		_ = h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
			Type: socket.MsgDeploymentPing,
			Payload: map[string]any{
				"deployment_id": deploymentID,
			},
		})

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

			err := h.WorkerHub.SendJSONToWorker(workerID, socket.Envelope{
				Type:    socket.MsgDeploy,
				Payload: retryPayload,
			})
			if err != nil {
				_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
					DeploymentID: deploymentID,
					Level:        "error",
					Message:      fmt.Sprintf("No deployment progress detected; retry %d/%d failed to dispatch: %v", attempt, deployMaxRetryCount, err),
				})
				continue
			}

			_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
				DeploymentID: deploymentID,
				Level:        "warning",
				Message:      fmt.Sprintf("No deployment progress detected for %s; retrying deployment (%d/%d)", deployStallTimeout, attempt, deployMaxRetryCount),
			})
			lastProgressAt = time.Now().UTC()
			continue
		}

		_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
			DeploymentID: deploymentID,
			Level:        "error",
			Message:      fmt.Sprintf("Deployment marked failed after %d stalled attempts with no progress", deployMaxRetryCount),
		})

		tx, txErr := db.BeginTx()
		if txErr != nil {
			logger.Error("deploy", "monitor failed to start transaction", logger.F{"deployment_id": deploymentID, "error": txErr})
			return
		}
		defer tx.Rollback()
		if err := query.UpdateDeploymentAndStackStatus(tx, deploymentID, "failed", stackID, "failed"); err != nil {
			logger.Error("deploy", "monitor failed to update status", logger.F{"deployment_id": deploymentID, "error": err})
			return
		}
		_ = tx.Commit()
		return
	}
}
