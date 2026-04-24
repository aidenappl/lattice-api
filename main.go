package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aidenappl/lattice-api/bootstrap"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/healthscan"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/retention"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/versions"
	"github.com/aidenappl/lattice-api/watcher"
	"github.com/aidenappl/lattice-api/webhooks"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

//go:embed install/runner.sh
var installRunnerScript []byte

// Set via -ldflags at build time: -X main.Version=<value>
// In CI this is set to the git tag (release builds) or short SHA (dev builds).
var Version = "dev"

func main() {
	// Initialize structured logger
	logger.Init(env.LogLevel, env.LogFormat)

	logger.Info("server", fmt.Sprintf("Lattice API %s starting", Version))
	routers.InstallScript = installRunnerScript
	routers.APIVersion = Version

	// 0. Validate security configuration before proceeding
	env.ValidateSecurityDefaults()

	// Start background polling for latest GitHub releases.
	versions.Start()

	// 1. Database
	db.Init()

	// 1b. Encryption
	crypto.Init()

	// Start background data retention cleanup.
	retention.Start(db.DB)

	// Start background image version watcher (polls registries for tag changes).
	watcher.Start()
	if err := db.PingDB(db.DB); err != nil {
		log.Fatal("failed to ping db: ", err)
	}

	// 2. Bootstrap admin user
	if err := bootstrap.EnsureAdminUser(db.DB); err != nil {
		log.Fatal("failed to bootstrap admin: ", err)
	}

	// 2b. Backfill networks from stored compose YAML for stacks that have no networks yet
	routers.BackfillNetworksFromCompose(db.DB)

	// 3. SSO setup (optional)
	if sso.IsConfigured() {
		logger.Info("sso", "configured")
	} else {
		logger.Info("sso", "not configured (local auth only)")
	}

	// 4. WebSocket hubs
	workerHub := socket.NewWorkerHub()
	adminHub := socket.NewAdminHub()

	// 4b. Health scanner — periodically audits worker/container state
	scanner := healthscan.New(db.DB, adminHub, workerHub)
	scanner.Start()
	routers.HealthScanner = scanner

	workerHandler := socket.NewWorkerHandler(workerHub)
	workerHandler.AuthFunc = func(r *http.Request) (int, bool) {
		return middleware.WorkerTokenAuth(r)
	}

	// Worker lifecycle:
	workerHandler.OnConnect = func(session *socket.WorkerSession) {
		logger.Info("worker", "connected", logger.F{"worker_id": session.WorkerID})
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
		// Cancel any pending disconnect alert (worker reconnected within grace period)
		mailer.CancelDisconnectAlert(session.WorkerID)
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_connected",
			"worker_id": session.WorkerID,
		})
	}

	// On disconnect, mark worker offline and fire webhook. We don't attempt to distinguish between graceful shutdowns and crashes here since in both cases the worker is offline and needs attention. The frontend can determine if it was a crash vs shutdown based on recent lifecycle logs and the presence of a shutdown message.
	workerHandler.OnDisconnect = func(session *socket.WorkerSession, err error) {
		logger.Info("worker", "disconnected", logger.F{"worker_id": session.WorkerID})
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "offline")
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_disconnected",
			"worker_id": session.WorkerID,
		})
		// Fetch worker name for richer notifications
		workerName := fmt.Sprintf("Worker %d", session.WorkerID)
		if w, wErr := query.GetWorkerByID(db.DB, session.WorkerID); wErr == nil {
			workerName = w.Name
		}
		webhooks.Fire("worker.disconnected", map[string]any{
			"worker_id":   session.WorkerID,
			"worker_name": workerName,
		})
		// Schedule disconnect alert with configurable grace period
		wID := session.WorkerID
		wName := workerName
		mailer.ScheduleDisconnectAlert(wID, func() {
			mailer.Notify("worker.disconnected", "Worker Disconnected",
				fmt.Sprintf("%s has gone offline.\n\nThe WebSocket connection to this worker was lost. This could be caused by a network interruption, a restart, or a crash.\n\nCheck the worker status in the Lattice dashboard for more details.", wName))
		})
	}

	//  Handle incoming messages from workers. These include heartbeats, container status updates, deployment progress, and more. We process some messages synchronously (like heartbeats and lifecycle logs) to ensure the database is updated before broadcasting to the frontend, while others are processed asynchronously to optimize for lower latency.
	workerHandler.OnMessage = func(session *socket.WorkerSession, msg socket.IncomingMessage) {
		switch msg.Type {
		case socket.MsgHeartbeat:
			_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
			// Persist runner_version if included so it stays current after upgrades.
			if rv, ok := msg.Payload["runner_version"].(string); ok && rv != "" {
				_ = query.UpdateWorkerRunnerVersion(db.DB, session.WorkerID, rv)
			}
			handleHeartbeatMetrics(session.WorkerID, msg.Payload)
			// Feed container state to health scanner
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
			// Mirror per-container deployment steps as lifecycle_log entries
			// so they appear in each container's log stream.
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
			// Write lifecycle logs synchronously BEFORE broadcasting so the DB
			// row exists when the frontend receives the event and calls loadLogs().
			enriched := handleContainerStatus(session.WorkerID, msg.Payload)
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_status",
				"worker_id": session.WorkerID,
				"payload":   enriched,
			})
			// Fire webhook on error status or stop/kill actions
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
				// Only alert after consecutive threshold is reached
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
			// Detect orphaned containers from failed deploys
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
			// Persist action status and clear on completion
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
			// Write lifecycle logs synchronously BEFORE broadcasting.
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
			// Write lifecycle logs synchronously BEFORE broadcasting.
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

	adminHandler := socket.NewAdminHandler(adminHub)

	// Authenticate admin WebSocket connections using the same dual-auth logic.
	adminHandler.AuthFunc = func(r *http.Request) (*structs.User, bool) {
		user, ok := middleware.GetUserFromContext(r.Context())
		return user, ok && user != nil
	}

	// Handle admin client messages (exec forwarding)
	adminHandler.OnMessage = func(session *socket.AdminSession, msg socket.IncomingMessage) {
		switch msg.Type {
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

	// Deploy handler (needs hub references)
	deployHandler := &routers.DeployHandler{
		WorkerHub: workerHub,
		AdminHub:  adminHub,
	}

	// Container action handler (needs hub references)
	containerActionHandler := &routers.ContainerActionHandler{
		WorkerHub: workerHub,
	}

	// Worker action handler (needs hub references)
	workerActionHandler := &routers.WorkerActionHandler{
		WorkerHub: workerHub,
	}

	// Volume handler (needs hub references)
	volumeHandler := &routers.VolumeHandler{
		WorkerHub: workerHub,
	}

	// Network handler (needs hub references)
	networkHandler := &routers.NetworkHandler{
		WorkerHub: workerHub,
	}

	// 5. Router
	r := mux.NewRouter()

	r.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.DB.Ping(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"degraded","db":"error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","db":"ok"}`))
	}).Methods(http.MethodGet)

	r.Use(middleware.RateLimitMiddleware)
	r.Use(middleware.RequestIDMiddleware)
	r.Use(middleware.LoggingMiddleware)
	r.Use(middleware.MuxHeaderMiddleware)
	r.Use(middleware.SecurityHeadersMiddleware)
	r.Use(middleware.CSRFMiddleware)
	r.Use(middleware.MaxBodySize(1 << 20)) // 1MB default body limit (compose import has its own higher limit)

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Lattice API"))
	}).Methods(http.MethodGet)

	// Version (public)
	r.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
	}).Methods(http.MethodGet)

	// Install script (public)
	r.HandleFunc("/install/runner", routers.HandleInstallRunner).Methods(http.MethodGet)

	// CI/CD deploy (public, token-authenticated)
	r.HandleFunc("/api/deploy/{token}", deployHandler.HandlePublicDeploy).Methods(http.MethodPost)

	// Auth routes (unprotected)
	r.HandleFunc("/auth/login", routers.HandleLocalLogin).Methods(http.MethodPost)
	r.HandleFunc("/auth/refresh", routers.HandleAuthRefresh).Methods(http.MethodPost)

	// SSO routes (always registered — config can change at runtime via DB)
	r.HandleFunc("/auth/sso/login", sso.LoginHandler).Methods(http.MethodGet)
	r.HandleFunc("/auth/sso/callback", routers.HandleSSOCallback).Methods(http.MethodGet)

	// SSO config endpoint (public — frontend uses this to show/hide SSO button)
	r.HandleFunc("/auth/sso/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sso.Config())
	}).Methods(http.MethodGet)

	// Auth self (protected - works with both local and SSO auth)
	authRouter := r.PathPrefix("/auth").Subrouter()
	authRouter.Use(middleware.DualAuthMiddleware)
	authRouter.HandleFunc("/self", routers.HandleAuthSelf).Methods(http.MethodGet)
	authRouter.HandleFunc("/self", routers.HandleUpdateSelf).Methods(http.MethodPut)
	authRouter.HandleFunc("/logout", routers.HandleLogout).Methods(http.MethodPost)

	// Admin routes (protected)
	admin := r.PathPrefix("/admin").Subrouter()
	admin.Use(middleware.DualAuthMiddleware)
	admin.Use(middleware.RejectPending)

	// Workers
	admin.HandleFunc("/workers", routers.HandleGetWorkers).Methods(http.MethodGet)
	admin.HandleFunc("/workers", middleware.RequireEditor(routers.HandleCreateWorker)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}", routers.HandleGetWorker).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}", middleware.RequireEditor(routers.HandleUpdateWorker)).Methods(http.MethodPut)
	admin.HandleFunc("/workers/{id}", middleware.RequireEditor(routers.HandleDeleteWorker)).Methods(http.MethodDelete)
	admin.HandleFunc("/workers/{id}/tokens", routers.HandleGetWorkerTokens).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/tokens", middleware.RequireEditor(routers.HandleCreateWorkerToken)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/metrics", routers.HandleGetWorkerMetrics).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/reboot", middleware.RequireAdmin(workerActionHandler.HandleRebootWorker)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/upgrade", middleware.RequireAdmin(workerActionHandler.HandleUpgradeRunner)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/stop-all", middleware.RequireEditor(workerActionHandler.HandleStopAllContainers)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/start-all", middleware.RequireEditor(workerActionHandler.HandleStartAllContainers)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/force-remove", middleware.RequireEditor(containerActionHandler.HandleForceRemoveContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/volumes", volumeHandler.HandleListVolumes).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/volumes", middleware.RequireEditor(volumeHandler.HandleCreateVolume)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/volumes/{name}", middleware.RequireEditor(volumeHandler.HandleDeleteVolume)).Methods(http.MethodDelete)
	admin.HandleFunc("/networks", routers.HandleListAllNetworks).Methods(http.MethodGet)
	admin.HandleFunc("/networks/{id}", middleware.RequireEditor(routers.HandleDeleteNetworkByID)).Methods(http.MethodDelete)
	admin.HandleFunc("/workers/{id}/networks", networkHandler.HandleListNetworks).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/networks", middleware.RequireEditor(networkHandler.HandleCreateNetwork)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/networks/{name}", middleware.RequireEditor(networkHandler.HandleDeleteNetwork)).Methods(http.MethodDelete)
	admin.HandleFunc("/worker-tokens/{id}", middleware.RequireEditor(routers.HandleDeleteWorkerToken)).Methods(http.MethodDelete)

	// Stacks
	admin.HandleFunc("/stacks", routers.HandleGetStacks).Methods(http.MethodGet)
	admin.HandleFunc("/stacks", middleware.RequireEditor(routers.HandleCreateStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/import", middleware.RequireEditor(routers.HandleImportCompose)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}", routers.HandleGetStack).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}", middleware.RequireEditor(routers.HandleUpdateStack)).Methods(http.MethodPut)
	admin.HandleFunc("/stacks/{id}", middleware.RequireEditor(containerActionHandler.HandleDeleteStack)).Methods(http.MethodDelete)
	admin.HandleFunc("/stacks/{id}/compose", middleware.RequireEditor(routers.HandleUpdateCompose)).Methods(http.MethodPut)
	admin.HandleFunc("/stacks/{id}/sync-compose", middleware.RequireEditor(routers.HandleSyncCompose)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/deploy", middleware.RequireEditor(deployHandler.HandleDeployStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/restart-all", middleware.RequireEditor(containerActionHandler.HandleRestartStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/stop-all", middleware.RequireEditor(containerActionHandler.HandleStopStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/start-all", middleware.RequireEditor(containerActionHandler.HandleStartStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/export", routers.HandleExportStack).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/import-export", middleware.RequireEditor(routers.HandleImportStackExport)).Methods(http.MethodPost)

	// Deploy tokens
	admin.HandleFunc("/stacks/{id}/deploy-tokens", routers.HandleListDeployTokens).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}/deploy-tokens", middleware.RequireAdmin(routers.HandleCreateDeployToken)).Methods(http.MethodPost)
	admin.HandleFunc("/deploy-tokens/{id}", middleware.RequireAdmin(routers.HandleDeleteDeployToken)).Methods(http.MethodDelete)

	// Containers
	admin.HandleFunc("/containers", routers.HandleListAllContainers).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}/containers", routers.HandleGetContainers).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}/containers", middleware.RequireEditor(routers.HandleCreateContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}", routers.HandleGetContainer).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}", middleware.RequireEditor(routers.HandleUpdateContainer)).Methods(http.MethodPut)
	admin.HandleFunc("/containers/{id}", middleware.RequireEditor(containerActionHandler.HandleDeleteContainer)).Methods(http.MethodDelete)
	admin.HandleFunc("/containers/{id}/logs", routers.HandleGetContainerLogs).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/lifecycle", routers.HandleGetLifecycleLogs).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/metrics", routers.HandleGetContainerMetrics).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/start", middleware.RequireEditor(containerActionHandler.HandleStartContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/stop", middleware.RequireEditor(containerActionHandler.HandleStopContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/kill", middleware.RequireEditor(containerActionHandler.HandleKillContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/restart", middleware.RequireEditor(containerActionHandler.HandleRestartContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/pause", middleware.RequireEditor(containerActionHandler.HandlePauseContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/unpause", middleware.RequireEditor(containerActionHandler.HandleUnpauseContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/remove", middleware.RequireEditor(containerActionHandler.HandleRemoveContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/recreate", middleware.RequireEditor(containerActionHandler.HandleRecreateContainer)).Methods(http.MethodPost)

	// Deployments
	admin.HandleFunc("/deployments", routers.HandleGetDeployments).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}", routers.HandleGetDeployment).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}/logs", routers.HandleGetDeploymentLogs).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}/approve", middleware.RequireEditor(routers.HandleApproveDeployment)).Methods(http.MethodPost)
	admin.HandleFunc("/deployments/{id}/rollback", middleware.RequireEditor(deployHandler.HandleRollbackDeployment)).Methods(http.MethodPost)

	// Registries
	admin.HandleFunc("/registries", routers.HandleGetRegistries).Methods(http.MethodGet)
	admin.HandleFunc("/registries", middleware.RequireEditor(routers.HandleCreateRegistry)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/test", middleware.RequireEditor(routers.HandleTestRegistryInline)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}", middleware.RequireEditor(routers.HandleUpdateRegistry)).Methods(http.MethodPut)
	admin.HandleFunc("/registries/{id}", middleware.RequireEditor(routers.HandleDeleteRegistry)).Methods(http.MethodDelete)
	admin.HandleFunc("/registries/{id}/test", middleware.RequireEditor(routers.HandleTestRegistry)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}/repositories", routers.HandleListRegistryRepos).Methods(http.MethodGet)
	admin.HandleFunc("/registries/{id}/tags", routers.HandleListRegistryTags).Methods(http.MethodGet)

	// Users
	admin.HandleFunc("/users", middleware.RequireAdmin(routers.HandleGetUsers)).Methods(http.MethodGet)
	admin.HandleFunc("/users", middleware.RequireAdmin(routers.HandleCreateUser)).Methods(http.MethodPost)
	admin.HandleFunc("/users/{id}", middleware.RequireAdmin(routers.HandleUpdateUser)).Methods(http.MethodPut)
	admin.HandleFunc("/users/{id}", middleware.RequireAdmin(routers.HandleDeleteUser)).Methods(http.MethodDelete)

	// Webhooks
	admin.HandleFunc("/webhooks", middleware.RequireAdmin(routers.HandleListWebhooks)).Methods(http.MethodGet)
	admin.HandleFunc("/webhooks", middleware.RequireAdmin(routers.HandleCreateWebhook)).Methods(http.MethodPost)
	admin.HandleFunc("/webhooks/{id}", middleware.RequireAdmin(routers.HandleUpdateWebhook)).Methods(http.MethodPut)
	admin.HandleFunc("/webhooks/{id}", middleware.RequireAdmin(routers.HandleDeleteWebhook)).Methods(http.MethodDelete)
	admin.HandleFunc("/webhooks/{id}/test", middleware.RequireAdmin(routers.HandleTestWebhook)).Methods(http.MethodPost)

	// Global Environment Variables
	admin.HandleFunc("/env-vars", routers.HandleListGlobalEnvVars).Methods(http.MethodGet)
	admin.HandleFunc("/env-vars", middleware.RequireAdmin(routers.HandleCreateGlobalEnvVar)).Methods(http.MethodPost)
	admin.HandleFunc("/env-vars/{id}", middleware.RequireAdmin(routers.HandleUpdateGlobalEnvVar)).Methods(http.MethodPut)
	admin.HandleFunc("/env-vars/{id}", middleware.RequireAdmin(routers.HandleDeleteGlobalEnvVar)).Methods(http.MethodDelete)

	// Templates
	admin.HandleFunc("/templates", routers.HandleListTemplates).Methods(http.MethodGet)
	admin.HandleFunc("/templates", middleware.RequireEditor(routers.HandleCreateTemplate)).Methods(http.MethodPost)
	admin.HandleFunc("/templates/{id}", middleware.RequireEditor(routers.HandleDeleteTemplate)).Methods(http.MethodDelete)
	admin.HandleFunc("/stacks/{id}/save-template", middleware.RequireEditor(routers.HandleCreateTemplateFromStack)).Methods(http.MethodPost)

	// Audit log
	admin.HandleFunc("/audit-log", middleware.RequireAdmin(routers.HandleGetAuditLog)).Methods(http.MethodGet)

	// SSO Configuration
	admin.HandleFunc("/sso-config", middleware.RequireAdmin(routers.HandleGetSSOConfig)).Methods(http.MethodGet)
	admin.HandleFunc("/sso-config", middleware.RequireAdmin(routers.HandleUpdateSSOConfig)).Methods(http.MethodPut)

	// SMTP Configuration
	admin.HandleFunc("/smtp-config", middleware.RequireAdmin(routers.HandleGetSMTPConfig)).Methods(http.MethodGet)
	admin.HandleFunc("/smtp-config", middleware.RequireAdmin(routers.HandleUpdateSMTPConfig)).Methods(http.MethodPut)
	admin.HandleFunc("/smtp-config/test", middleware.RequireAdmin(routers.HandleTestSMTP)).Methods(http.MethodPost)

	// Notification preferences
	admin.HandleFunc("/notification-prefs", middleware.RequireAdmin(routers.HandleGetNotificationPrefs)).Methods(http.MethodGet)
	admin.HandleFunc("/notification-prefs", middleware.RequireAdmin(routers.HandleUpdateNotificationPrefs)).Methods(http.MethodPut)

	// Search
	admin.HandleFunc("/search", routers.HandleSearch).Methods(http.MethodGet)

	// Overview (dashboard)
	admin.HandleFunc("/overview", routers.HandleGetOverview).Methods(http.MethodGet)
	admin.HandleFunc("/fleet-metrics", routers.HandleGetFleetMetrics).Methods(http.MethodGet)
	admin.HandleFunc("/anomalies", routers.HandleGetAnomalies).Methods(http.MethodGet)

	// Versions & updates
	admin.HandleFunc("/versions", routers.HandleGetVersions).Methods(http.MethodGet)
	admin.HandleFunc("/versions/refresh", middleware.RequireAdmin(routers.HandleRefreshVersions)).Methods(http.MethodPost)
	admin.HandleFunc("/update/api", middleware.RequireAdmin(routers.HandleUpdateAPI)).Methods(http.MethodPost)
	admin.HandleFunc("/update/web", middleware.RequireAdmin(routers.HandleUpdateWeb)).Methods(http.MethodPost)

	// WebSocket endpoints
	r.Handle("/ws/worker", workerHandler).Methods(http.MethodGet)
	r.Handle("/ws/admin", middleware.DualAuthMiddleware(adminHandler)).Methods(http.MethodGet)

	// 6. CORS
	allowedOrigins := []string{"http://localhost:3000"}
	if env.AllowedOrigins != "" {
		allowedOrigins = append(allowedOrigins, strings.Split(env.AllowedOrigins, ",")...)
	}

	// Configure WebSocket origin validation with the same allowed origins.
	socket.AllowedOrigins = allowedOrigins

	corsMiddleware := cors.New(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowCredentials: true,
		AllowedHeaders: []string{
			"X-Requested-With",
			"Content-Type",
			"Origin",
			"Authorization",
			"Accept",
			"Cookie",
			"X-CSRF-Token",
		},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
	})

	// 7. Server with graceful shutdown
	server := &http.Server{
		Addr:         ":" + env.Port,
		Handler:      corsMiddleware.Handler(r),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if env.TLSCert != "" && env.TLSKey != "" {
			logger.Info("server", "listening", logger.F{"port": env.Port, "tls": true})
			if err := server.ListenAndServeTLS(env.TLSCert, env.TLSKey); err != nil && err != http.ErrServerClosed {
				log.Fatal("server error: ", err)
			}
		} else {
			logger.Info("server", "listening", logger.F{"port": env.Port, "tls": false})
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal("server error: ", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("server", "shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("server forced to shutdown: ", err)
	}
	logger.Info("server", "stopped")
}

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

