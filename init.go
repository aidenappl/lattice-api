package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/aidenappl/lattice-api/bootstrap"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/healthscan"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/migrate"
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
	workerHub     *socket.WorkerHub
	adminHub      *socket.AdminHub
	scanner       *healthscan.Scanner
	workerHandler *socket.WorkerHandler
	adminHandler  *socket.AdminHandler

	deployHandler          *routers.DeployHandler
	containerActionHandler *routers.ContainerActionHandler
	workerActionHandler    *routers.WorkerActionHandler
	volumeHandler          *routers.VolumeHandler
	networkHandler         *routers.NetworkHandler
	databaseHandler        *routers.DatabaseHandler
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

	// Schema migrations run before anything is served, so a deploy is
	// self-contained: a migration file only exists inside the image that carries
	// it, and the code depending on a new column ships in that same image.
	//
	// A failure here is loud but NOT fatal, and that is a deliberate departure
	// from forta-api's runner, which this was ported from. The argument for fatal
	// is that a rolling recreate aborts the deploy and the previous container
	// keeps serving. lattice-api does not get that: it updates *itself* through
	// the control plane it is, so a failed migration is not an aborted deploy —
	// it is a crashloop that takes the control plane, its dashboard and its own
	// rollback path offline together. That is exactly what happened on v1.3.25,
	// from nothing worse than a bookkeeping table with the wrong shape.
	//
	// So: degrade rather than disappear. The API starts on the old schema, the
	// failure is unmissable in the logs, and `migrate` — where a failure aborts a
	// deliberate action and nothing is serving — stays fatal.
	if err := db.RunMigrations(); err != nil {
		logger.Error("database", "SCHEMA MIGRATIONS FAILED — starting on the existing schema; "+
			"features depending on new columns will not work until this is resolved", logger.F{
			"error": err.Error(),
			"fix":   "run `lattice-api migrate` against this database and read the error",
		})
	}

	crypto.Init()
	retention.Start(db.DB)
	watcher.Start()
	mailer.StartEviction()
	containerNameCache.StartEviction()

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

	// Database lifecycle owner and reconciler. The lifecycle owner must exist
	// before any worker message is dispatched, since every database status
	// write goes through it.
	dbLifecycle = &databaseLifecycle{adminHub: adminHub}
	dbReconciler = newDatabaseReconciler(workerHub)
	dbReconciler.Start()

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
		databaseHandler: &routers.DatabaseHandler{
			WorkerHub: workerHub,
			AdminHub:  adminHub,
			Lifecycle: dbLifecycle,
		},
	}
}

// runMigrate applies pending schema migrations and exits, without starting the
// server or any background loop.
//
// Migrations also run on boot (see initApp), which is what makes a deploy
// self-contained. This subcommand exists for the cases where that is not enough:
// applying a change ahead of a deploy, and `migrate status` to see what a
// database actually has without changing it.
func runMigrate(args []string) {
	logger.Init(env.LogLevel, env.LogFormat)

	if len(args) > 0 && args[0] == "status" {
		statuses, err := db.MigrationsStatus()
		if err != nil {
			fmt.Printf("❌ failed to read migration status: %v\n", err)
			os.Exit(1)
		}
		pending := 0
		for _, s := range statuses {
			mark := "✅ applied"
			if !s.Applied {
				mark = "⏳ pending"
				pending++
			}
			fmt.Printf("  %s  %s\n", mark, s.Name)
		}
		fmt.Printf("\n%d migration(s), %d pending\n", len(statuses), pending)
		return
	}

	fmt.Println("Applying schema migrations...")
	if err := db.RunMigrations(); err != nil {
		fmt.Printf("❌ migration failed: %v\n", err)
		os.Exit(1)
	}
}

// runMigrateEncrypt runs the one-off plaintext->ciphertext secret migration and
// exits. It performs the minimal init the migration needs (logger, DB, crypto)
// instead of the full server boot, so it never starts watchers, hubs, or the
// HTTP listener. Requires ENCRYPTION_KEY to be set (crypto.Init panics otherwise
// in production, and RunEncrypt refuses to run if the key is inactive).
func runMigrateEncrypt(args []string) {
	dryRun := false
	for _, a := range args {
		if a == "--dry-run" || a == "-dry-run" {
			dryRun = true
		}
	}

	logger.Init(env.LogLevel, env.LogFormat)
	db.Init()
	crypto.Init()

	fmt.Printf("lattice-api migrate-encrypt (dry-run=%v)\n\n", dryRun)
	if err := migrate.RunEncrypt(dryRun); err != nil {
		log.Fatalf("migrate-encrypt failed: %v", err)
	}
	if dryRun {
		fmt.Println("\ndry-run complete — no changes written. Re-run without --dry-run to apply.")
	} else {
		fmt.Println("\nmigrate-encrypt complete — secrets encrypted at rest.")
	}
	os.Exit(0)
}
