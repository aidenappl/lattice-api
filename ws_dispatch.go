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
	"github.com/aidenappl/lattice-api/routers"
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

		// Distribute database snapshot schedules to the reconnected worker
		safeGo("db-schedule-sync", func() {
			routers.DistributeDbSchedules(session.WorkerID, wh.Hub)
		})

		// Ask the worker what database containers it actually has before
		// trusting anything stored about them. Anything that changed while it
		// was offline is corrected now rather than up to a reconcile interval
		// later.
		safeGo("db-sync-request", func() {
			dbReconciler.RequestSync(session.WorkerID)
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
				fmt.Sprintf("<strong>%s</strong> has gone offline.\n\nThe WebSocket connection to this worker was lost. This could be caused by a network interruption, a restart, or a crash.\n\nCheck the worker status in the Lattice dashboard for more details.", wName))
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

		// ── Database management responses ─────────────────────────────────
		case socket.MsgDbStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-status", func() { handleDbStatus(session.WorkerID, msg.Payload) })

		case socket.MsgDbHealthStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_health_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-health", func() {
				instanceID := payloadInt(msg.Payload, socket.PayloadDbInstanceID)
				hs, _ := msg.Payload["health_status"].(string)
				if instanceID == 0 || hs == "" {
					return
				}
				message, _ := msg.Payload["message"].(string)
				dbLifecycle.SetHealth(instanceID, structs.DatabaseHealth(hs), message)
			})

		case socket.MsgDbMirrorStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_mirror_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-mirror", func() { handleMirrorStatus(msg.Payload) })

		case socket.MsgDbSync:
			// Full per-worker report of observed database containers. This is
			// the level-triggered input the reconciler diffs against desired
			// state, so a lost db_status can no longer strand an instance.
			safeGo("db-sync", func() { handleDbSync(session.WorkerID, msg.Payload) })

		case socket.MsgDbSnapshotProgress:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_snapshot_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-snapshot", func() {
				// snapshot_id arrives as a JSON number; payloadInt also tolerates
				// the string form older runners sent.
				snapshotID := payloadInt(msg.Payload, socket.PayloadSnapshotID)
				status, _ := msg.Payload[socket.PayloadStatus].(string)
				if status == "" {
					return
				}

				// A scheduled snapshot has no row yet — the runner's own cron
				// started it, so nothing created one. Adopt it by
				// (database_instance_id, filename), which is why the runner
				// computes the filename before it can fail.
				if snapshotID == 0 {
					instanceID := payloadInt(msg.Payload, socket.PayloadDbInstanceID)
					filename, _ := msg.Payload[socket.PayloadFilename].(string)
					if instanceID == 0 || filename == "" {
						logger.Warn("database", "snapshot status with neither snapshot_id nor (instance,filename) — dropping", logger.F{
							"worker_id": session.WorkerID,
							"status":    status,
						})
						return
					}
					snapshot, err := ensureScheduledSnapshotRow(instanceID, filename)
					if err != nil {
						logger.Error("database", "failed to adopt scheduled snapshot", logger.F{
							"database_instance_id": instanceID, "filename": filename, "error": err,
						})
						return
					}
					snapshotID = snapshot.ID
				}
				var sizeBytes *int64
				if sb, ok := msg.Payload["size_bytes"].(float64); ok && sb > 0 {
					v := int64(sb)
					sizeBytes = &v
				}
				var errMsg *string
				if em, ok := msg.Payload["error_message"].(string); ok && em != "" {
					errMsg = &em
				}
				if err := query.UpdateSnapshotStatus(db.DB, snapshotID, status, sizeBytes, errMsg); err != nil {
					logger.Error("database", "failed to update snapshot status", logger.F{
						"snapshot_id": snapshotID, "status": status, "error": err,
					})
					return
				}
				instanceID := payloadInt(msg.Payload, socket.PayloadDbInstanceID)
				if instanceID != 0 && status == "failed" {
					detail := "snapshot failed"
					if errMsg != nil {
						detail = *errMsg
					}
					dbLifecycle.RecordEvent(instanceID, structs.DBEventFailed, "snapshot "+detail, "worker")
				}

				// A completed snapshot is the moment retention becomes
				// meaningful: there is a new copy, so the oldest may go. It is
				// also proof the schedule is alive again.
				// Close the scheduled run this snapshot belongs to, if any. A run
				// left open blocks every later slot via skip-on-overrun.
				if status == "completed" || status == "failed" {
					closeRunForSnapshot(snapshotID, status == "completed")
				}

				if status == "completed" {
					recordPrimaryReplicaAndMirror(snapshotID, sizeBytes, wh.Hub)
				}

				if instanceID != 0 && status == "completed" {
					dbLifecycle.ClearWarning(instanceID, structs.DBErrCodeBackupStale)
					// A delete that was waiting on this snapshot can now proceed.
					// Retention is skipped in that case: the instance is on its
					// way out, and expiring an old copy while destroying the
					// database would be exactly the wrong moment.
					if !finaliseDeleteAfterSnapshot(instanceID, wh.Hub) {
						applySnapshotRetention(instanceID, wh.Hub)
					}
				}
			})

		case socket.MsgDbRestoreStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_restore_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-restore", func() {
				instanceID := payloadInt(msg.Payload, socket.PayloadDbInstanceID)
				status, _ := msg.Payload["status"].(string)
				if instanceID == 0 || status == "" {
					return
				}
				message, _ := msg.Payload["error_message"].(string)
				if message == "" {
					message = "restore " + status
				}
				kind := structs.DBEventTransition
				if status == "failed" {
					kind = structs.DBEventFailed
				}
				dbLifecycle.RecordEvent(instanceID, kind, message, "worker")
			})

		case socket.MsgDbDeleteSnapshotResult:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_delete_snapshot_result",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-delete-snapshot", func() {
				snapshotID := payloadInt(msg.Payload, "snapshot_id")
				status, _ := msg.Payload["status"].(string)
				if snapshotID == 0 {
					return
				}
				// The row is soft-deleted optimistically when the command is
				// issued; a failure here means the remote file outlived it.
				if status == "failed" || status == "error" {
					detail, _ := msg.Payload["message"].(string)
					logger.Warn("database", "worker failed to delete snapshot file", logger.F{
						"snapshot_id": snapshotID, "message": detail,
					})
				}
			})

		case socket.MsgDbScheduleStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "db_schedule_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
			safeGo("db-schedule-status", func() {
				instanceID := payloadInt(msg.Payload, socket.PayloadDbInstanceID)
				status, _ := msg.Payload["status"].(string)
				if instanceID == 0 {
					return
				}
				message, _ := msg.Payload["message"].(string)
				if message == "" {
					message = "snapshot schedule " + status
				}
				kind := structs.DBEventTransition
				if status == "failed" || status == "error" {
					kind = structs.DBEventFailed
				}
				dbLifecycle.RecordEvent(instanceID, kind, message, "worker")
			})

		case socket.MsgBackupDestTestResult:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "backup_dest_test_result",
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
		if !ok || user == nil {
			return nil, false
		}
		// Pending users must never get the admin stream or the exec relay.
		if user.Role == "pending" {
			return nil, false
		}
		return user, true
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
			// Exec relays a container shell to a worker — this is effectively RCE.
			// Gate it to editor+ (reject viewer/pending). The session role is
			// captured from the authenticated user at connect time.
			if session.Role != "admin" && session.Role != "editor" {
				logger.Warn("socket", "rejected exec relay for insufficient role", logger.F{"session_id": session.ID, "role": session.Role, "type": msg.Type})
				return
			}
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
