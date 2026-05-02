package main

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/gorilla/mux"
)

//go:embed install/runner.sh
var installRunnerScript []byte

// Set via -ldflags at build time: -X main.Version=<value>
var Version = "dev"

func main() {
	app := initApp()

	r := mux.NewRouter()

	// Health check (before middleware)
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

	// Global middleware
	r.Use(middleware.RateLimitMiddleware)
	r.Use(middleware.RequestIDMiddleware)
	r.Use(middleware.LoggingMiddleware)
	r.Use(middleware.MuxHeaderMiddleware)
	r.Use(middleware.SecurityHeadersMiddleware)
	r.Use(middleware.CSRFMiddleware)
	r.Use(middleware.MaxBodySize(1 << 20)) // 1MB default body limit

	// Public routes
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Lattice API"))
	}).Methods(http.MethodGet)

	r.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
	}).Methods(http.MethodGet)

	r.HandleFunc("/install/runner", routers.HandleInstallRunner).Methods(http.MethodGet)

	// CI/CD deploy (public, token-authenticated)
	r.HandleFunc("/api/deploy/{token}", app.deployHandler.HandlePublicDeploy).Methods(http.MethodPost)

	// Auth (unprotected)
	r.HandleFunc("/auth/login", routers.HandleLocalLogin).Methods(http.MethodPost)
	r.HandleFunc("/auth/refresh", routers.HandleAuthRefresh).Methods(http.MethodPost)
	r.HandleFunc("/auth/sso/login", sso.LoginHandler).Methods(http.MethodGet)
	r.HandleFunc("/auth/sso/callback", routers.HandleSSOCallback).Methods(http.MethodGet)
	r.HandleFunc("/auth/sso/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sso.Config())
	}).Methods(http.MethodGet)

	// Auth (protected)
	authRouter := r.PathPrefix("/auth").Subrouter()
	authRouter.Use(middleware.DualAuthMiddleware)
	authRouter.HandleFunc("/self", routers.HandleAuthSelf).Methods(http.MethodGet)
	authRouter.HandleFunc("/self", routers.HandleUpdateSelf).Methods(http.MethodPut)
	authRouter.HandleFunc("/logout", routers.HandleLogout).Methods(http.MethodPost)

	// ── Admin routes ─────────────────────────────────────────────────────────
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
	admin.HandleFunc("/workers/{id}/container-stats", routers.HandleGetWorkerContainerStats).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/reboot", middleware.RequireAdmin(app.workerActionHandler.HandleRebootWorker)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/upgrade", middleware.RequireAdmin(app.workerActionHandler.HandleUpgradeRunner)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/stop-all", middleware.RequireEditor(app.workerActionHandler.HandleStopAllContainers)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/start-all", middleware.RequireEditor(app.workerActionHandler.HandleStartAllContainers)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/force-remove", middleware.RequireEditor(app.containerActionHandler.HandleForceRemoveContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/volumes", app.volumeHandler.HandleListVolumes).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/volumes", middleware.RequireEditor(app.volumeHandler.HandleCreateVolume)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/volumes/{name}", middleware.RequireEditor(app.volumeHandler.HandleDeleteVolume)).Methods(http.MethodDelete)
	admin.HandleFunc("/workers/{id}/networks", app.networkHandler.HandleListNetworks).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/networks", middleware.RequireEditor(app.networkHandler.HandleCreateNetwork)).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/networks/{name}", middleware.RequireEditor(app.networkHandler.HandleDeleteNetwork)).Methods(http.MethodDelete)
	admin.HandleFunc("/worker-tokens/{id}", middleware.RequireEditor(routers.HandleDeleteWorkerToken)).Methods(http.MethodDelete)

	// Networks
	admin.HandleFunc("/networks", routers.HandleListAllNetworks).Methods(http.MethodGet)
	admin.HandleFunc("/networks/{id}", middleware.RequireEditor(routers.HandleDeleteNetworkByID)).Methods(http.MethodDelete)

	// Stacks
	admin.HandleFunc("/stacks", routers.HandleGetStacks).Methods(http.MethodGet)
	admin.HandleFunc("/stacks", middleware.RequireEditor(routers.HandleCreateStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/import", middleware.RequireEditor(routers.HandleImportCompose)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}", routers.HandleGetStack).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}", middleware.RequireEditor(routers.HandleUpdateStack)).Methods(http.MethodPut)
	admin.HandleFunc("/stacks/{id}", middleware.RequireEditor(app.containerActionHandler.HandleDeleteStack)).Methods(http.MethodDelete)
	admin.HandleFunc("/stacks/{id}/compose", middleware.RequireEditor(routers.HandleUpdateCompose)).Methods(http.MethodPut)
	admin.HandleFunc("/stacks/{id}/sync-compose", middleware.RequireEditor(routers.HandleSyncCompose)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/deploy", middleware.RequireEditor(app.deployHandler.HandleDeployStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/restart-all", middleware.RequireEditor(app.containerActionHandler.HandleRestartStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/stop-all", middleware.RequireEditor(app.containerActionHandler.HandleStopStack)).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}/start-all", middleware.RequireEditor(app.containerActionHandler.HandleStartStack)).Methods(http.MethodPost)
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
	admin.HandleFunc("/containers/{id}", middleware.RequireEditor(app.containerActionHandler.HandleDeleteContainer)).Methods(http.MethodDelete)
	admin.HandleFunc("/containers/{id}/logs", routers.HandleGetContainerLogs).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/lifecycle", routers.HandleGetLifecycleLogs).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/metrics", routers.HandleGetContainerMetrics).Methods(http.MethodGet)
	admin.HandleFunc("/containers/{id}/start", middleware.RequireEditor(app.containerActionHandler.HandleStartContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/stop", middleware.RequireEditor(app.containerActionHandler.HandleStopContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/kill", middleware.RequireEditor(app.containerActionHandler.HandleKillContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/restart", middleware.RequireEditor(app.containerActionHandler.HandleRestartContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/pause", middleware.RequireEditor(app.containerActionHandler.HandlePauseContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/unpause", middleware.RequireEditor(app.containerActionHandler.HandleUnpauseContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/remove", middleware.RequireEditor(app.containerActionHandler.HandleRemoveContainer)).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}/recreate", middleware.RequireEditor(app.containerActionHandler.HandleRecreateContainer)).Methods(http.MethodPost)

	// Deployments
	admin.HandleFunc("/deployments", routers.HandleGetDeployments).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}", routers.HandleGetDeployment).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}/logs", routers.HandleGetDeploymentLogs).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}/approve", middleware.RequireEditor(routers.HandleApproveDeployment)).Methods(http.MethodPost)
	admin.HandleFunc("/deployments/{id}/rollback", middleware.RequireEditor(app.deployHandler.HandleRollbackDeployment)).Methods(http.MethodPost)

	// Registries
	admin.HandleFunc("/registries", routers.HandleGetRegistries).Methods(http.MethodGet)
	admin.HandleFunc("/registries", middleware.RequireEditor(routers.HandleCreateRegistry)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/test", middleware.RequireEditor(routers.HandleTestRegistryInline)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}", middleware.RequireEditor(routers.HandleUpdateRegistry)).Methods(http.MethodPut)
	admin.HandleFunc("/registries/{id}", middleware.RequireEditor(routers.HandleDeleteRegistry)).Methods(http.MethodDelete)
	admin.HandleFunc("/registries/{id}/test", middleware.RequireEditor(routers.HandleTestRegistry)).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}/repositories", routers.HandleListRegistryRepos).Methods(http.MethodGet)
	admin.HandleFunc("/registries/{id}/tags", routers.HandleListRegistryTags).Methods(http.MethodGet)

	// Database Instances
	admin.HandleFunc("/database-instances", routers.HandleListDatabaseInstances).Methods(http.MethodGet)
	admin.HandleFunc("/database-instances", middleware.RequireEditor(app.databaseHandler.HandleCreateDatabaseInstance)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}", routers.HandleGetDatabaseInstance).Methods(http.MethodGet)
	admin.HandleFunc("/database-instances/{id}", middleware.RequireEditor(app.databaseHandler.HandleUpdateDatabaseInstance)).Methods(http.MethodPut)
	admin.HandleFunc("/database-instances/{id}", middleware.RequireEditor(app.databaseHandler.HandleDeleteDatabaseInstance)).Methods(http.MethodDelete)
	admin.HandleFunc("/database-instances/{id}/start", middleware.RequireEditor(app.databaseHandler.HandleDatabaseAction)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}/stop", middleware.RequireEditor(app.databaseHandler.HandleDatabaseAction)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}/restart", middleware.RequireEditor(app.databaseHandler.HandleDatabaseAction)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}/remove", middleware.RequireEditor(app.databaseHandler.HandleDatabaseAction)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}/credentials", middleware.RequireEditor(app.databaseHandler.HandleGetDatabaseCredentials)).Methods(http.MethodGet)
	admin.HandleFunc("/database-instances/{id}/snapshots", routers.HandleListSnapshots).Methods(http.MethodGet)
	admin.HandleFunc("/database-instances/{id}/snapshots", middleware.RequireEditor(app.databaseHandler.HandleCreateSnapshot)).Methods(http.MethodPost)
	admin.HandleFunc("/database-instances/{id}/restore", middleware.RequireEditor(app.databaseHandler.HandleRestoreSnapshot)).Methods(http.MethodPost)
	admin.HandleFunc("/database-snapshots/{id}", middleware.RequireEditor(routers.HandleDeleteSnapshot)).Methods(http.MethodDelete)

	// Backup Destinations
	admin.HandleFunc("/backup-destinations", routers.HandleListBackupDestinations).Methods(http.MethodGet)
	admin.HandleFunc("/backup-destinations", middleware.RequireEditor(routers.HandleCreateBackupDestination)).Methods(http.MethodPost)
	admin.HandleFunc("/backup-destinations/{id}", routers.HandleGetBackupDestination).Methods(http.MethodGet)
	admin.HandleFunc("/backup-destinations/{id}", middleware.RequireEditor(routers.HandleUpdateBackupDestination)).Methods(http.MethodPut)
	admin.HandleFunc("/backup-destinations/{id}", middleware.RequireEditor(routers.HandleDeleteBackupDestination)).Methods(http.MethodDelete)
	admin.HandleFunc("/backup-destinations/{id}/test", middleware.RequireEditor(app.databaseHandler.HandleTestBackupDestination)).Methods(http.MethodPost)

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
	r.Handle("/ws/worker", app.workerHandler).Methods(http.MethodGet)
	r.Handle("/ws/admin", middleware.DualAuthMiddleware(app.adminHandler)).Methods(http.MethodGet)

	startServer(r)
}
