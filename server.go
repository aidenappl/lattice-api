package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

// startServer configures CORS, creates the HTTP server, and blocks until
// a SIGINT/SIGTERM triggers graceful shutdown.
func startServer(r *mux.Router) {
	allowedOrigins := []string{"http://localhost:3000"}
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
