package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/aidenappl/lattice-api/bootstrap"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/healthscan"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/retention"
	"github.com/aidenappl/lattice-api/routers"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/versions"
	"github.com/aidenappl/lattice-api/watcher"
)

// appContext holds all runtime dependencies created during initialization.
// It is passed to route registration and server startup.
type appContext struct {
	workerHub    *socket.WorkerHub
	adminHub     *socket.AdminHub
	scanner      *healthscan.Scanner
	workerHandler *socket.WorkerHandler
	adminHandler  *socket.AdminHandler

	deployHandler          *routers.DeployHandler
	containerActionHandler *routers.ContainerActionHandler
	workerActionHandler    *routers.WorkerActionHandler
	volumeHandler          *routers.VolumeHandler
	networkHandler         *routers.NetworkHandler
}

// initApp bootstraps all services, creates WebSocket hubs and handler structs,
// and returns an appContext ready for route registration.
func initApp() *appContext {
	logger.Init(env.LogLevel, env.LogFormat)
	logger.Info("server", fmt.Sprintf("Lattice API %s starting", Version))

	routers.InstallScript = installRunnerScript
	routers.APIVersion = Version

	env.ValidateSecurityDefaults()
	versions.Start()

	db.Init()
	crypto.Init()
	retention.Start(db.DB)
	watcher.Start()

	if err := db.PingDB(db.DB); err != nil {
		log.Fatal("failed to ping db: ", err)
	}

	if err := bootstrap.EnsureAdminUser(db.DB); err != nil {
		log.Fatal("failed to bootstrap admin: ", err)
	}

	routers.BackfillNetworksFromCompose(db.DB)

	if sso.IsConfigured() {
		logger.Info("sso", "configured")
	} else {
		logger.Info("sso", "not configured (local auth only)")
	}

	// WebSocket hubs
	workerHub := socket.NewWorkerHub()
	adminHub := socket.NewAdminHub()

	// Health scanner
	scanner := healthscan.New(db.DB, adminHub, workerHub)
	scanner.Start()
	routers.HealthScanner = scanner

	// Worker WebSocket handler
	workerHandler := socket.NewWorkerHandler(workerHub)
	workerHandler.AuthFunc = func(r *http.Request) (int, bool) {
		return middleware.WorkerTokenAuth(r)
	}
	configureWorkerHandler(workerHandler, adminHub, scanner)

	// Admin WebSocket handler
	adminHandler := socket.NewAdminHandler(adminHub)
	configureAdminHandler(adminHandler, workerHub)

	return &appContext{
		workerHub:     workerHub,
		adminHub:      adminHub,
		scanner:       scanner,
		workerHandler: workerHandler,
		adminHandler:  adminHandler,

		deployHandler: &routers.DeployHandler{
			WorkerHub: workerHub,
			AdminHub:  adminHub,
		},
		containerActionHandler: &routers.ContainerActionHandler{
			WorkerHub: workerHub,
		},
		workerActionHandler: &routers.WorkerActionHandler{
			WorkerHub: workerHub,
		},
		volumeHandler: &routers.VolumeHandler{
			WorkerHub: workerHub,
		},
		networkHandler: &routers.NetworkHandler{
			WorkerHub: workerHub,
		},
	}
}
