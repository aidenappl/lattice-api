package routers

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
)

const (
	deployPingInterval  = 15 * time.Second
	deployStallTimeout  = 45 * time.Second
	deployMaxRetryCount = 3
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
			log.Printf("[PANIC] deployment monitor %d: %v", deploymentID, r)
		}
	}()

	ticker := time.NewTicker(deployPingInterval)
	defer ticker.Stop()

	attempt := 1
	lastProgressAt := time.Now().UTC()

	for range ticker.C {
		dep, err := query.GetDeploymentByID(db.DB, deploymentID)
		if err != nil {
			log.Printf("deploy-monitor: failed to load deployment=%d: %v", deploymentID, err)
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

		failedStatus := "failed"
		_, _ = query.UpdateStack(db.DB, stackID, query.UpdateStackRequest{Status: &failedStatus})
		_ = query.UpdateDeploymentStatus(db.DB, deploymentID, "failed")
		return
	}
}
