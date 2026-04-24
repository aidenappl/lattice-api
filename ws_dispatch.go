package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/healthscan"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/webhooks"

	"net/http"

	"github.com/aidenappl/lattice-api/middleware"
)

// msgSem limits concurrent message handler goroutines to prevent unbounded
// goroutine growth under heavy WebSocket traffic.
var msgSem = make(chan struct{}, 100)

func safeGo(name string, fn func()) {
	msgSem <- struct{}{} // acquire semaphore
	go func() {
		defer func() { <-msgSem }() // release semaphore
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic", fmt.Sprintf("%v", r), logger.F{"goroutine": name})
			}
		}()
		fn()
	}()
}

// configureWorkerHandler sets up OnConnect, OnDisconnect, and OnMessage
// callbacks for the worker WebSocket handler.
func configureWorkerHandler(wh *socket.WorkerHandler, adminHub *socket.AdminHub, scanner *healthscan.Scanner) {
	wh.OnConnect = func(session *socket.WorkerSession) {
		logger.Info("worker", "connected", logger.F{"worker_id": session.WorkerID})
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
		mailer.CancelDisconnectAlert(session.WorkerID)
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_connected",
			"worker_id": session.WorkerID,
		})
	}

	wh.OnDisconnect = func(session *socket.WorkerSession, err error) {
		logger.Info("worker", "disconnected", logger.F{"worker_id": session.WorkerID})
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "offline")
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_disconnected",
			"worker_id": session.WorkerID,
		})
		workerName := fmt.Sprintf("Worker %d", session.WorkerID)
		if w, wErr := query.GetWorkerByID(db.DB, session.WorkerID); wErr == nil {
			workerName = w.Name
		}
		webhooks.Fire("worker.disconnected", map[string]any{
			"worker_id":   session.WorkerID,
			"worker_name": workerName,
		})
		wID := session.WorkerID
		wName := workerName
		mailer.ScheduleDisconnectAlert(wID, func() {
			mailer.Notify("worker.disconnected", "Worker Disconnected",
				fmt.Sprintf("%s has gone offline.\n\nThe WebSocket connection to this worker was lost. This could be caused by a network interruption, a restart, or a crash.\n\nCheck the worker status in the Lattice dashboard for more details.", wName))
		})
	}

	wh.OnMessage = func(session *socket.WorkerSession, msg socket.IncomingMessage) {
		switch msg.Type {
		case socket.MsgHeartbeat:
			_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
			if rv, ok := msg.Payload["runner_version"].(string); ok && rv != "" {
				_ = query.UpdateWorkerRunnerVersion(db.DB, session.WorkerID, rv)
			}
			handleHeartbeatMetrics(session.WorkerID, msg.Payload)
			if names := healthscan.ParseContainerNames(msg.Payload); len(names) > 0 {
				scanner.UpdateWorkerContainers(session.WorkerID, names)
			}
			adminHub.BroadcastJSON(map[string]any{
				"type":      "worker_heartbeat",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgRegistration:
			osStr, _ := msg.Payload["os"].(string)
			arch, _ := msg.Payload["arch"].(string)
			dockerVersion, _ := msg.Payload["docker_version"].(string)
			ipAddress, _ := msg.Payload["ip_address"].(string)
			runnerVersion, _ := msg.Payload["runner_version"].(string)

			// Resolve pending upgrade action on reconnect
			if runnerVersion != "" {
				if worker, err := query.GetWorkerByID(db.DB, session.WorkerID); err == nil && worker.PendingAction != nil {
					var pa map[string]string
					if json.Unmarshal([]byte(*worker.PendingAction), &pa) == nil && pa["action"] == "upgrade_runner" {
						oldVersion := ""
						if worker.RunnerVersion != nil {
							oldVersion = *worker.RunnerVersion
						}
						_ = query.SetWorkerPendingAction(db.DB, session.WorkerID, nil)
						status := "success"
						message := fmt.Sprintf("upgraded to %s", runnerVersion)
						if runnerVersion == oldVersion {
							status = "failed"
							message = "runner restarted with same version"
						}
						adminHub.BroadcastJSON(map[string]any{
							"type":      "worker_action_status",
							"worker_id": session.WorkerID,
							"payload": map[string]any{
								"action":  "upgrade_runner",
								"status":  status,
								"message": message,
							},
						})
					}
				}
			}

			_ = query.UpdateWorkerInfo(db.DB, session.WorkerID, osStr, arch, dockerVersion, ipAddress, runnerVersion)

		case socket.MsgDeploymentProgress:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "deployment_progress",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			if cn, _ := msg.Payload["container_name"].(string); cn != "" {
				if message, _ := msg.Payload["message"].(string); message != "" {
					canonical := stripDeploySuffix(cn)
					lcPayload := map[string]any{
						"container_name": canonical,
						"event":          "deploy",
						"message":        message,
					}
					handleLifecycleLog(session.WorkerID, lcPayload)
					adminHub.BroadcastJSON(map[string]any{
						"type":      "lifecycle_log",
						"worker_id": session.WorkerID,
						"payload":   lcPayload,
					})
				}
			}
			handleDeploymentProgress(msg.Payload)

		case socket.MsgDeploymentStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "deployment_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			if depID, ok := msg.Payload["deployment_id"].(float64); ok {
				message, _ := msg.Payload["message"].(string)
				if message != "" {
					stage := "status_check"
					_ = query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
						DeploymentID: int(depID),
						Level:        "info",
						Stage:        &stage,
						Message:      fmt.Sprintf("Runner status check: %s", message),
					})
				}
			}

		case socket.MsgContainerStatus:
			enriched := handleContainerStatus(session.WorkerID, msg.Payload)
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_status",
				"worker_id": session.WorkerID,
				"payload":   enriched,
			})
			if action, _ := msg.Payload["action"].(string); action == "stop" || action == "kill" {
				webhooks.Fire("container.status", enriched)
			}
			if status, _ := msg.Payload["status"].(string); status == "error" {
				webhooks.Fire("container.status", enriched)
			}

		case socket.MsgContainerHealthStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_health_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("container-health", func() { handleContainerHealthStatus(msg.Payload) })
			hs, _ := msg.Payload["health_status"].(string)
			cName, _ := msg.Payload["container_name"].(string)
			if hs == "unhealthy" {
				webhooks.Fire("container.unhealthy", msg.Payload)
				if mailer.TrackUnhealthy(cName) && mailer.ShouldAlert("container.unhealthy", cName) {
					mailer.Notify("container.unhealthy", "Container Unhealthy",
						fmt.Sprintf("Container <strong>%s</strong> is failing its health check.\n\nThe container's health status has changed to unhealthy. This typically means the health check command is returning a non-zero exit code.\n\nReview the container logs and health check configuration in the Lattice dashboard.", cName))
				}
			} else if hs == "healthy" && cName != "" {
				mailer.ClearUnhealthy(cName)
			}

		case socket.MsgContainerSync:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_sync",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			if containerName, _ := msg.Payload["container_name"].(string); containerName != "" {
				if strings.Contains(containerName, "-retired-") ||
					strings.HasSuffix(containerName, "-lattice-retired") ||
					strings.HasSuffix(containerName, "-lattice-updating") {
					latticeStatus, _ := msg.Payload["status"].(string)
					adminHub.BroadcastJSON(map[string]any{
						"type":      "orphaned_container",
						"worker_id": session.WorkerID,
						"payload": map[string]any{
							"container_name": containerName,
							"status":         latticeStatus,
						},
					})
				}
			}
			safeGo("container-sync", func() { handleContainerSync(msg.Payload) })

		case socket.MsgContainerLogs:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_logs",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("container-log", func() { handleContainerLog(session.WorkerID, msg.Payload) })

		case socket.MsgLifecycleLog:
			handleLifecycleLog(session.WorkerID, msg.Payload)
			adminHub.BroadcastJSON(map[string]any{
				"type":      "lifecycle_log",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgWorkerActionStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "worker_action_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			actionName, _ := msg.Payload["action"].(string)
			actionStatus, _ := msg.Payload["status"].(string)
			actionMessage, _ := msg.Payload["message"].(string)
			if actionName == "upgrade_runner" || actionName == "reboot_os" {
				if actionStatus == "success" || actionStatus == "failed" || actionStatus == "error" {
					_ = query.SetWorkerPendingAction(db.DB, session.WorkerID, nil)
				} else {
					actionData := map[string]string{
						"action":     actionName,
						"status":     actionStatus,
						"message":    actionMessage,
						"started_at": time.Now().UTC().Format(time.RFC3339),
					}
					actionBytes, _ := json.Marshal(actionData)
					actionJSON := string(actionBytes)
					_ = query.SetWorkerPendingAction(db.DB, session.WorkerID, &actionJSON)
				}
			}

		case socket.MsgWorkerShutdown:
			reason, _ := msg.Payload["reason"].(string)
			message, _ := msg.Payload["message"].(string)
			logger.Info("worker", "shutting down gracefully", logger.F{"worker_id": session.WorkerID, "reason": reason})
			writeWorkerLifecycleLogs(session.WorkerID, "worker_shutdown", message)
			adminHub.BroadcastJSON(map[string]any{
				"type":      "worker_shutdown",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgWorkerCrash:
			goroutine, _ := msg.Payload["goroutine"].(string)
			panicMsg, _ := msg.Payload["panic"].(string)
			logger.Error("worker", "crash detected", logger.F{"worker_id": session.WorkerID, "goroutine": goroutine, "panic": panicMsg})
			crashMsg := fmt.Sprintf("worker crashed: %s (goroutine: %s)", panicMsg, goroutine)
			writeWorkerLifecycleLogs(session.WorkerID, "worker_crash", crashMsg)
			adminHub.BroadcastJSON(map[string]any{
				"type":      "worker_crash",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			webhooks.Fire("worker.crash", map[string]any{
				"worker_id": session.WorkerID,
				"goroutine": goroutine,
				"panic":     panicMsg,
			})
			workerCrashName := fmt.Sprintf("Worker %d", session.WorkerID)
			if w, wErr := query.GetWorkerByID(db.DB, session.WorkerID); wErr == nil {
				workerCrashName = w.Name
			}
			if mailer.ShouldAlert("worker.crash", fmt.Sprintf("%d", session.WorkerID)) {
				mailer.Notify("worker.crash", "Worker Crashed",
					fmt.Sprintf("<strong>%s</strong> experienced an unrecoverable panic.\n\n<strong>Goroutine:</strong> %s\n<strong>Panic:</strong> %s\n\nThe runner process has crashed and will need to be restarted. If the runner is configured as a systemd service, it should restart automatically.", workerCrashName, goroutine, panicMsg))
			}

		case socket.MsgListVolumesResponse:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "list_volumes_response",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgListNetworksResponse:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "list_networks_response",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgExecOutput:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "exec_output",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
		}
	}
}

// configureAdminHandler sets up authentication and message handling
// for the admin WebSocket handler.
func configureAdminHandler(ah *socket.AdminHandler, workerHub *socket.WorkerHub) {
	ah.AuthFunc = func(r *http.Request) (*structs.User, bool) {
		user, ok := middleware.GetUserFromContext(r.Context())
		return user, ok && user != nil
	}

	ah.OnMessage = func(session *socket.AdminSession, msg socket.IncomingMessage) {
		switch msg.Type {
		case socket.MsgSubscribe:
			if topics, ok := msg.Payload["topics"].([]any); ok {
				strs := make([]string, 0, len(topics))
				for _, t := range topics {
					if s, ok := t.(string); ok {
						strs = append(strs, s)
					}
				}
				session.Subscribe(strs)
			}

		case socket.MsgUnsubscribe:
			if topics, ok := msg.Payload["topics"].([]any); ok {
				strs := make([]string, 0, len(topics))
				for _, t := range topics {
					if s, ok := t.(string); ok {
						strs = append(strs, s)
					}
				}
				session.Unsubscribe(strs)
			} else {
				session.Unsubscribe(nil)
			}

		case socket.MsgExecStart, socket.MsgExecInput, socket.MsgExecResize, socket.MsgExecClose:
			workerIDFloat, _ := msg.Payload["worker_id"].(float64)
			workerID := int(workerIDFloat)
			if workerID == 0 {
				return
			}
			_ = workerHub.SendJSONToWorker(workerID, socket.Envelope{
				Type:      msg.Type,
				CommandID: msg.CommandID,
				Payload:   msg.Payload,
			})
		}
	}
}
