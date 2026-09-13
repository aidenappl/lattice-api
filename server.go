package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/telemetry"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

// startServer configures CORS, creates the HTTP server, and blocks until
// a SIGINT/SIGTERM triggers graceful shutdown.
func startServer(r *mux.Router) {
	// Only trust the localhost dev origin outside production — in production it
	// would let a page on the developer's machine make credentialed cross-origin
	// requests against the live API. Production origins come from ALLOWED_ORIGINS.
	var allowedOrigins []string
	if env.Environment != "production" {
		allowedOrigins = append(allowedOrigins, "http://localhost:3000")
	}
	if env.AllowedOrigins != "" {
		allowedOrigins = append(allowedOrigins, strings.Split(env.AllowedOrigins, ",")...)
	}

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
		// Readable by lattice-web, which puts it on its own Monitor events so a
		// browser failure and the API request behind it share one id.
		ExposedHeaders: []string{"X-Request-ID"},
	})

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
				telemetry.Fatal("service.listen_failed", "server error: ", err)
			}
		} else {
			logger.Info("server", "listening", logger.F{"port": env.Port, "tls": false})
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				telemetry.Fatal("service.listen_failed", "server error: ", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.Info("server", "shutting down...", logger.F{"signal": sig.String()})
	// 7s rather than 10: Docker's default stop timeout is 10s, and the telemetry
	// flush below needs what is left of it. WebSockets are hijacked and are not
	// waited on either way.
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server", "graceful shutdown incomplete", logger.F{"error": err})
	}
	logger.Info("server", "stopped")
	telemetry.Shutdown(sig.String())
}
