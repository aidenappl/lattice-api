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

	forta "github.com/aidenappl/go-forta"
	"github.com/aidenappl/lattice-api/bootstrap"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/retention"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/socket"
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
	fmt.Printf("Lattice API %s\n\n", Version)
	routers.InstallScript = installRunnerScript
	routers.APIVersion = Version

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

	// 3. Forta setup (optional)
	fortaEnabled := false
	if env.FortaAPIDomain != "" && env.FortaClientID != "" {
		fmt.Print("Connecting to Forta...")
		if err := forta.Setup(forta.Config{
			APIDomain:          env.FortaAPIDomain,
			AppDomain:          env.FortaAppDomain,
			LoginDomain:        env.FortaLoginDomain,
			ClientID:           env.FortaClientID,
			ClientSecret:       env.FortaClientSecret,
			CallbackURL:        env.FortaCallbackURL,
			PostLoginRedirect:  env.FortaPostLoginRedirect,
			PostLogoutRedirect: env.FortaPostLogoutRedirect,
			CookieDomain:       env.FortaCookieDomain,
			CookieInsecure:     env.FortaCookieInsecure,
			JWTSigningKey:      env.FortaJWTSigningKey,
			FetchUserOnProtect: env.FortaFetchUserOnProtect,
			DisableAutoRefresh: env.FortaDisableAutoRefresh,
			EnforceGrants:      env.FortaEnforceGrants,
		}); err != nil {
			fmt.Println(" ⚠️  Failed (running without Forta)")
			log.Println("forta setup error:", err)
		} else {
			if err := forta.Ping(); err != nil {
				fmt.Println(" ⚠️  Unreachable (running without Forta)")
				log.Println("forta ping error:", err)
			} else {
				fmt.Println(" ✅ Done")
				fortaEnabled = true
			}
		}
	} else {
		fmt.Println("Forta: not configured (local auth only)")
	}

	// 4. WebSocket hubs
	workerHub := socket.NewWorkerHub()
	adminHub := socket.NewAdminHub()

	workerHandler := socket.NewWorkerHandler(workerHub)
	workerHandler.AuthFunc = func(r *http.Request) (int, bool) {
		return middleware.WorkerTokenAuth(r)
	}

	workerHandler.OnConnect = func(session *socket.WorkerSession) {
		log.Printf("worker=%d connected", session.WorkerID)
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_connected",
			"worker_id": session.WorkerID,
		})
	}

	workerHandler.OnDisconnect = func(session *socket.WorkerSession, err error) {
		log.Printf("worker=%d disconnected", session.WorkerID)
		_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "offline")
		adminHub.BroadcastJSON(map[string]any{
			"type":      "worker_disconnected",
			"worker_id": session.WorkerID,
		})
		webhooks.Fire("worker.disconnected", map[string]any{
			"worker_id": session.WorkerID,
		})
	}

	workerHandler.OnMessage = func(session *socket.WorkerSession, msg socket.IncomingMessage) {
		switch msg.Type {
		case socket.MsgHeartbeat:
			_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
			// Persist runner_version if included so it stays current after upgrades.
			if rv, ok := msg.Payload["runner_version"].(string); ok && rv != "" {
				_ = query.UpdateWorkerRunnerVersion(db.DB, session.WorkerID, rv)
				// Clear any pending upgrade action — runner is alive and reporting its version
				_ = query.SetWorkerPendingAction(db.DB, session.WorkerID, nil)
			}
			handleHeartbeatMetrics(session.WorkerID, msg.Payload)
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
			_ = query.UpdateWorkerInfo(db.DB, session.WorkerID, osStr, arch, dockerVersion, ipAddress, runnerVersion)

		case socket.MsgDeploymentProgress:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "deployment_progress",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
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
			if hs, _ := msg.Payload["health_status"].(string); hs == "unhealthy" {
				webhooks.Fire("container.unhealthy", msg.Payload)
			}

		case socket.MsgContainerSync:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_sync",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
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
			log.Printf("worker=%d shutting down gracefully: reason=%s", session.WorkerID, reason)
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
			log.Printf("worker=%d CRASH in goroutine %q: %s", session.WorkerID, goroutine, panicMsg)
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
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	r.Use(middleware.RateLimitMiddleware)
	r.Use(middleware.RequestIDMiddleware)
	r.Use(middleware.LoggingMiddleware)
	r.Use(middleware.MuxHeaderMiddleware)
	r.Use(middleware.SecurityHeadersMiddleware)
	r.Use(middleware.CSRFMiddleware)

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Lattice API"))
	}).Methods(http.MethodGet)

	// Version (public)
	r.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":"%s"}`, Version)
	}).Methods(http.MethodGet)

	// Install script (public)
	r.HandleFunc("/install/runner", routers.HandleInstallRunner).Methods(http.MethodGet)

	// CI/CD deploy (public, token-authenticated)
	r.HandleFunc("/api/deploy/{token}", deployHandler.HandlePublicDeploy).Methods(http.MethodPost)

	// Auth routes (unprotected)
	r.HandleFunc("/auth/login", routers.HandleLocalLogin).Methods(http.MethodPost)
	r.HandleFunc("/auth/refresh", routers.HandleAuthRefresh).Methods(http.MethodPost)

	// Forta routes (conditional)
	if fortaEnabled {
		r.HandleFunc("/forta/login", forta.LoginHandler).Methods(http.MethodGet)
		r.HandleFunc("/forta/callback", forta.CallbackHandler).Methods(http.MethodGet)
		r.HandleFunc("/forta/logout", forta.LogoutHandler).Methods(http.MethodGet)
	}

	// Auth self (protected - works with both local and Forta auth)
	authRouter := r.PathPrefix("/auth").Subrouter()
	authRouter.Use(middleware.DualAuthMiddleware)
	authRouter.HandleFunc("/self", routers.HandleAuthSelf).Methods(http.MethodGet)
	authRouter.HandleFunc("/logout", routers.HandleLogout).Methods(http.MethodPost)

	// Admin routes (protected)
	admin := r.PathPrefix("/admin").Subrouter()
	admin.Use(middleware.DualAuthMiddleware)

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
	admin.HandleFunc("/workers/{id}/volumes", volumeHandler.HandleListVolumes).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/volumes", middleware.RequireEditor(volumeHandler.HandleCreateVolume)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/volumes/{name}", middleware.RequireEditor(volumeHandler.HandleDeleteVolume)).Methods(http.MethodDelete)
	admin.HandleFunc("/networks", routers.HandleListAllNetworks).Methods(http.MethodGet)
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
	admin.HandleFunc("/webhooks", routers.HandleListWebhooks).Methods(http.MethodGet)
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

	// Overview (dashboard)
	admin.HandleFunc("/overview", routers.HandleGetOverview).Methods(http.MethodGet)
	admin.HandleFunc("/fleet-metrics", routers.HandleGetFleetMetrics).Methods(http.MethodGet)

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
			fmt.Printf("Lattice API running (HTTPS) on port %s\n\n", env.Port)
			if err := server.ListenAndServeTLS(env.TLSCert, env.TLSKey); err != nil && err != http.ErrServerClosed {
				log.Fatal("server error: ", err)
			}
		} else {
			fmt.Printf("Lattice API running on port %s\n\n", env.Port)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal("server error: ", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("server forced to shutdown: ", err)
	}
	log.Println("server stopped")
}

func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC] %s: %v", name, r)
			}
		}()
		fn()
	}()
}

func handleHeartbeatMetrics(workerID int, payload map[string]any) {
	req := query.CreateMetricsRequest{WorkerID: workerID}

	extractFloat := func(key string) *float64 {
		if v, ok := payload[key].(float64); ok {
			return &v
		}
		return nil
	}
	extractInt := func(key string) *int {
		if v, ok := payload[key].(float64); ok {
			i := int(v)
			return &i
		}
		return nil
	}
	extractInt64 := func(key string) *int64 {
		if v, ok := payload[key].(float64); ok {
			i := int64(v)
			return &i
		}
		return nil
	}

	req.CPUPercent = extractFloat("cpu_percent")
	req.CPUCores = extractInt("cpu_cores")
	req.LoadAvg1 = extractFloat("load_avg_1")
	req.LoadAvg5 = extractFloat("load_avg_5")
	req.LoadAvg15 = extractFloat("load_avg_15")
	req.MemoryUsedMB = extractFloat("memory_used_mb")
	req.MemoryTotalMB = extractFloat("memory_total_mb")
	req.MemoryFreeMB = extractFloat("memory_free_mb")
	req.SwapUsedMB = extractFloat("swap_used_mb")
	req.SwapTotalMB = extractFloat("swap_total_mb")
	req.DiskUsedMB = extractFloat("disk_used_mb")
	req.DiskTotalMB = extractFloat("disk_total_mb")
	req.ContainerCount = extractInt("container_count")
	req.ContainerRunningCount = extractInt("container_running_count")
	req.NetworkRxBytes = extractInt64("network_rx_bytes")
	req.NetworkTxBytes = extractInt64("network_tx_bytes")
	req.UptimeSeconds = extractFloat("uptime_seconds")
	req.ProcessCount = extractInt("process_count")

	if err := query.CreateMetrics(db.DB, req); err != nil {
		log.Printf("failed to store heartbeat metrics for worker=%d: %v", workerID, err)
	}
}

func handleDeploymentProgress(payload map[string]any) {
	deploymentID, ok := payload["deployment_id"].(float64)
	if !ok {
		return
	}
	status, _ := payload["status"].(string)
	message, _ := payload["message"].(string)
	step, _ := payload["step"].(string)
	containerName, _ := payload["container_name"].(string)

	// Determine log level from status
	level := "info"
	if status == "failed" {
		level = "error"
	}

	// Build a descriptive stage
	var stage *string
	if step != "" {
		s := step
		if containerName != "" {
			s = containerName + ":" + step
		}
		stage = &s
	} else if containerName != "" {
		stage = &containerName
	}

	// Build the log message
	logMsg := message
	if logMsg == "" {
		logMsg = fmt.Sprintf("status=%s", status)
	}

	log.Printf("deploy[%d]: [%s] %s (stage=%v)", int(deploymentID), level, logMsg, stage)

	// Store deployment log
	if err := query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: int(deploymentID),
		Level:        level,
		Stage:        stage,
		Message:      logMsg,
	}); err != nil {
		log.Printf("failed to store deployment log: %v", err)
	}

	// Fire webhooks on deployment terminal states
	if status == "failed" {
		webhooks.Fire("deployment.failed", map[string]any{
			"deployment_id": int(deploymentID),
			"message":       message,
		})
	}
	if status == "deployed" {
		webhooks.Fire("deployment.success", map[string]any{
			"deployment_id": int(deploymentID),
			"message":       message,
		})
	}

	// Update deployment status if it's a terminal/state-change status
	if status == "deploying" || status == "deployed" || status == "failed" || status == "rolled_back" {
		if err := query.UpdateDeploymentStatus(db.DB, int(deploymentID), status); err != nil {
			log.Printf("failed to update deployment=%d status: %v", int(deploymentID), err)
		}

		// On terminal states, update stack status and all containers in the deployment
		if status == "deployed" || status == "failed" || status == "rolled_back" {
			// Update stack status to match
			dep, err := query.GetDeploymentByID(db.DB, int(deploymentID))
			if err != nil {
				log.Printf("failed to get deployment %d for stack status update: %v", int(deploymentID), err)
			} else {
				stackStatus := status
				if status == "rolled_back" {
					stackStatus = "failed"
				}
				if _, err := query.UpdateStack(db.DB, dep.StackID, query.UpdateStackRequest{Status: &stackStatus}); err != nil {
					log.Printf("failed to update stack %d status to %q: %v", dep.StackID, stackStatus, err)
				} else {
					log.Printf("updated stack %d status to %q", dep.StackID, stackStatus)
				}
			}

			// Update container statuses
			if status == "deployed" || status == "failed" {
				containerStatus := "running"
				if status == "failed" {
					containerStatus = "error"
				}
				dcs, err := query.ListDeploymentContainers(db.DB, int(deploymentID))
				if err != nil {
					log.Printf("failed to list deployment containers for status update: %v", err)
				} else if dcs != nil {
					for _, dc := range *dcs {
						s := containerStatus
						_, _ = query.UpdateContainer(db.DB, dc.ContainerID, query.UpdateContainerRequest{Status: &s})
					}
				}
			}
		}
	}
}

func handleContainerStatus(workerID int, payload map[string]any) map[string]any {
	containerName, _ := payload["container_name"].(string)
	action, _ := payload["action"].(string)
	status, _ := payload["status"].(string)

	// Always return the raw payload with enrichments for the admin broadcast
	enriched := map[string]any{
		"container_name": containerName,
		"action":         action,
		"status":         status,
	}

	if containerName == "" || status != "success" {
		return enriched
	}

	// Map action to container DB status
	var dbStatus string
	switch action {
	case "stop", "remove", "kill":
		dbStatus = "stopped"
	case "start", "restart", "recreate", "unpause":
		dbStatus = "running"
	case "pause":
		dbStatus = "paused"
	default:
		return enriched
	}

	c, err := query.GetContainerByName(db.DB, containerName)
	if err != nil {
		log.Printf("container status: could not find container %q: %v", containerName, err)
		return enriched
	}

	// Enrich with container_id and the resolved container state for the frontend
	enriched["container_id"] = c.ID
	enriched["container_state"] = dbStatus

	req := query.UpdateContainerRequest{Status: &dbStatus}
	// On start/recreate/restart, reset health_status to "starting" if the container has a healthcheck.
	if (action == "start" || action == "recreate" || action == "restart") && c.HealthCheck != nil {
		hs := "starting"
		req.HealthStatus = &hs
		enriched["health_status"] = hs
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, req); err != nil {
		log.Printf("container status: failed to update %q to %s: %v", containerName, dbStatus, err)
	} else {
		log.Printf("container status: %s → %s", containerName, dbStatus)

		// Write a lifecycle entry to lifecycle_logs so it persists in the log viewer.
		lifecycleMessages := map[string]string{
			"start":    "container started",
			"restart":  "container restarted",
			"stop":     "container stopped",
			"kill":     "container force-killed",
			"recreate": "container recreated",
			"pause":    "container paused",
			"unpause":  "container unpaused",
		}
		if msg, ok := lifecycleMessages[action]; ok {
			cID := c.ID
			cName := c.Name

			logReq := query.CreateLifecycleLogRequest{
				WorkerID:      workerID,
				ContainerID:   &cID,
				ContainerName: &cName,
				Event:         action,
				Message:       msg,
			}
			if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
				log.Printf("container status: failed to write lifecycle log for %q: %v", containerName, err)
			}
		}
	}

	return enriched
}

// handleLifecycleLog processes lifecycle_log messages from workers and persists
// them to the lifecycle_logs table. These are verbose progress messages sent
// during container actions (e.g. "pulling image…", "stopping container…").
func handleLifecycleLog(workerID int, payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	event, _ := payload["event"].(string)
	message, _ := payload["message"].(string)

	if message == "" {
		return
	}

	logReq := query.CreateLifecycleLogRequest{
		WorkerID: workerID,
		Event:    event,
		Message:  message,
	}

	if containerName != "" {
		logReq.ContainerName = &containerName
		if c, err := query.GetContainerByName(db.DB, containerName); err == nil {
			logReq.ContainerID = &c.ID
		}
	}

	if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
		log.Printf("lifecycle log: failed to write for container=%q event=%q: %v", containerName, event, err)
	}
}

// handleContainerHealthStatus processes health_status messages from workers.
func handleContainerHealthStatus(payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	healthStatus, _ := payload["health_status"].(string)
	if containerName == "" || healthStatus == "" {
		return
	}

	c, err := query.GetContainerByName(db.DB, containerName)
	if err != nil {
		log.Printf("container health: could not find container %q: %v", containerName, err)
		return
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, query.UpdateContainerRequest{HealthStatus: &healthStatus}); err != nil {
		log.Printf("container health: failed to update %q health_status to %s: %v", containerName, healthStatus, err)
	} else {
		log.Printf("container health: %s → %s", containerName, healthStatus)
	}
}

// handleContainerSync reconciles live Docker state sent every heartbeat.
// It only writes to DB if the state differs from what is stored.
func handleContainerSync(payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	latticeStatus, _ := payload["status"].(string)
	if containerName == "" || latticeStatus == "" {
		return
	}

	c, err := query.GetContainerByName(db.DB, containerName)
	if err != nil {
		// Container not managed by Lattice — ignore
		return
	}

	req := query.UpdateContainerRequest{}
	changed := false

	if c.Status != latticeStatus {
		req.Status = &latticeStatus
		changed = true
	}

	// If the container is no longer running (and not just paused), clear any stale health status.
	if latticeStatus != "running" && latticeStatus != "paused" && c.HealthStatus != "none" {
		none := "none"
		req.HealthStatus = &none
		changed = true
	}

	// If the worker reports a health_check config and we don't have one stored, persist it.
	if hcRaw, ok := payload["health_check"]; ok && hcRaw != nil && c.HealthCheck == nil {
		if hcBytes, err := json.Marshal(hcRaw); err == nil {
			hcStr := string(hcBytes)
			req.HealthCheck = &hcStr
			changed = true
		}
	}

	if !changed {
		return
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, req); err != nil {
		log.Printf("container sync: failed to update %q to %s: %v", containerName, latticeStatus, err)
	} else {
		log.Printf("container sync: %s → %s (was %s)", containerName, latticeStatus, c.Status)
	}
}

func handleContainerLog(workerID int, payload map[string]any) {
	message, ok := payload["message"].(string)
	if !ok || message == "" {
		return
	}

	req := query.CreateContainerLogRequest{
		WorkerID: workerID,
		Message:  message,
		Stream:   "stdout",
	}

	if v, ok := payload["stream"].(string); ok {
		req.Stream = v
	}

	// Resolve container_name to container_id and always store the name
	if name, ok := payload["container_name"].(string); ok && name != "" {
		req.ContainerName = &name
		if c, err := query.GetContainerByName(db.DB, name); err == nil {
			req.ContainerID = &c.ID
		} else {
			log.Printf("container log: could not resolve container name %q to ID: %v", name, err)
		}
	}

	// Use the Docker-recorded timestamp when provided by the runner so that
	// reconnect replays of the same line land on the same recorded_at value
	// and are silently dropped by the unique index in the DB.
	if ts, ok := payload["recorded_at"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			req.RecordedAt = &t
		}
	}

	if err := query.CreateContainerLog(db.DB, req); err != nil {
		log.Printf("failed to store container log for worker=%d: %v", workerID, err)
	}
}

// writeWorkerLifecycleLogs writes a system log entry to every container
// belonging to workerID. Used for shutdown and crash events so the log viewer
// shows what happened to the runner.
func writeWorkerLifecycleLogs(workerID int, event string, message string) {
	containers, err := query.ListAllContainers(db.DB, query.ListAllContainersRequest{WorkerID: &workerID})
	if err != nil || containers == nil {
		if err != nil {
			log.Printf("worker lifecycle log: failed to list containers for worker=%d: %v", workerID, err)
		}
		return
	}
	for _, c := range *containers {
		cID := c.ID
		cName := c.Name
		logReq := query.CreateLifecycleLogRequest{
			WorkerID:      workerID,
			ContainerID:   &cID,
			ContainerName: &cName,
			Event:         event,
			Message:       message,
		}
		if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
			log.Printf("worker lifecycle log: failed to write log for container %q: %v", cName, err)
		}
	}
}
