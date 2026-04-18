package main

import (
	"context"
	_ "embed"
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
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

//go:embed install/runner.sh
var installRunnerScript []byte

// Set via -ldflags at build time: -ldflags "-X main.Version=abc1234"
var Version = "v0.0.1"

func main() {
	fmt.Printf("Lattice API %s\n\n", Version)
	routers.InstallScript = installRunnerScript

	// 1. Database
	db.Init()
	if err := db.PingDB(db.DB); err != nil {
		log.Fatal("failed to ping db: ", err)
	}

	// 2. Bootstrap admin user
	if err := bootstrap.EnsureAdminUser(db.DB); err != nil {
		log.Fatal("failed to bootstrap admin: ", err)
	}

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
	}

	workerHandler.OnMessage = func(session *socket.WorkerSession, msg socket.IncomingMessage) {
		switch msg.Type {
		case socket.MsgHeartbeat:
			_ = query.UpdateWorkerHeartbeat(db.DB, session.WorkerID, "online")
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

		case socket.MsgContainerStatus:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_status",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})

		case socket.MsgContainerLogs:
			adminHub.BroadcastJSON(map[string]any{
				"type":      "container_logs",
				"worker_id": session.WorkerID,
				"payload":   msg.Payload,
			})
		}
	}

	adminHandler := socket.NewAdminHandler(adminHub)

	// Deploy handler (needs hub references)
	deployHandler := &routers.DeployHandler{
		WorkerHub: workerHub,
		AdminHub:  adminHub,
	}

	// 5. Router
	r := mux.NewRouter()

	r.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	r.Use(middleware.RequestIDMiddleware)
	r.Use(middleware.LoggingMiddleware)
	r.Use(middleware.MuxHeaderMiddleware)

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

	// Admin routes (protected)
	admin := r.PathPrefix("/admin").Subrouter()
	admin.Use(middleware.DualAuthMiddleware)

	// Workers
	admin.HandleFunc("/workers", routers.HandleGetWorkers).Methods(http.MethodGet)
	admin.HandleFunc("/workers", routers.HandleCreateWorker).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}", routers.HandleGetWorker).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}", routers.HandleUpdateWorker).Methods(http.MethodPut)
	admin.HandleFunc("/workers/{id}", routers.HandleDeleteWorker).Methods(http.MethodDelete)
	admin.HandleFunc("/workers/{id}/tokens", routers.HandleGetWorkerTokens).Methods(http.MethodGet)
	admin.HandleFunc("/workers/{id}/tokens", routers.HandleCreateWorkerToken).Methods(http.MethodPost)
	admin.HandleFunc("/workers/{id}/metrics", routers.HandleGetWorkerMetrics).Methods(http.MethodGet)
	admin.HandleFunc("/worker-tokens/{id}", routers.HandleDeleteWorkerToken).Methods(http.MethodDelete)

	// Stacks
	admin.HandleFunc("/stacks", routers.HandleGetStacks).Methods(http.MethodGet)
	admin.HandleFunc("/stacks", routers.HandleCreateStack).Methods(http.MethodPost)
	admin.HandleFunc("/stacks/{id}", routers.HandleGetStack).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}", routers.HandleUpdateStack).Methods(http.MethodPut)
	admin.HandleFunc("/stacks/{id}", routers.HandleDeleteStack).Methods(http.MethodDelete)
	admin.HandleFunc("/stacks/{id}/deploy", deployHandler.HandleDeployStack).Methods(http.MethodPost)

	// Containers
	admin.HandleFunc("/stacks/{id}/containers", routers.HandleGetContainers).Methods(http.MethodGet)
	admin.HandleFunc("/stacks/{id}/containers", routers.HandleCreateContainer).Methods(http.MethodPost)
	admin.HandleFunc("/containers/{id}", routers.HandleUpdateContainer).Methods(http.MethodPut)
	admin.HandleFunc("/containers/{id}", routers.HandleDeleteContainer).Methods(http.MethodDelete)

	// Deployments
	admin.HandleFunc("/deployments", routers.HandleGetDeployments).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}", routers.HandleGetDeployment).Methods(http.MethodGet)
	admin.HandleFunc("/deployments/{id}/approve", routers.HandleApproveDeployment).Methods(http.MethodPost)
	admin.HandleFunc("/deployments/{id}/rollback", routers.HandleRollbackDeployment).Methods(http.MethodPost)

	// Registries
	admin.HandleFunc("/registries", routers.HandleGetRegistries).Methods(http.MethodGet)
	admin.HandleFunc("/registries", routers.HandleCreateRegistry).Methods(http.MethodPost)
	admin.HandleFunc("/registries/test", routers.HandleTestRegistryInline).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}", routers.HandleUpdateRegistry).Methods(http.MethodPut)
	admin.HandleFunc("/registries/{id}", routers.HandleDeleteRegistry).Methods(http.MethodDelete)
	admin.HandleFunc("/registries/{id}/test", routers.HandleTestRegistry).Methods(http.MethodPost)
	admin.HandleFunc("/registries/{id}/repositories", routers.HandleListRegistryRepos).Methods(http.MethodGet)
	admin.HandleFunc("/registries/{id}/tags", routers.HandleListRegistryTags).Methods(http.MethodGet)

	// Users
	admin.HandleFunc("/users", routers.HandleGetUsers).Methods(http.MethodGet)
	admin.HandleFunc("/users", routers.HandleCreateUser).Methods(http.MethodPost)
	admin.HandleFunc("/users/{id}", routers.HandleUpdateUser).Methods(http.MethodPut)

	// Audit log
	admin.HandleFunc("/audit-log", routers.HandleGetAuditLog).Methods(http.MethodGet)

	// Overview (dashboard)
	admin.HandleFunc("/overview", routers.HandleGetOverview).Methods(http.MethodGet)

	// WebSocket endpoints
	r.Handle("/ws/worker", workerHandler).Methods(http.MethodGet)
	r.Handle("/ws/admin", adminHandler).Methods(http.MethodGet)

	// 6. CORS
	allowedOrigins := []string{"http://localhost:3000"}
	if env.AllowedOrigins != "" {
		allowedOrigins = append(allowedOrigins, strings.Split(env.AllowedOrigins, ",")...)
	}

	corsMiddleware := cors.New(cors.Options{
		AllowedOrigins: allowedOrigins,
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
	status, ok := payload["status"].(string)
	if !ok {
		return
	}

	if err := query.UpdateDeploymentStatus(db.DB, int(deploymentID), status); err != nil {
		log.Printf("failed to update deployment=%d status: %v", int(deploymentID), err)
	}
}
