package routers

import (
	"net/http"

	"github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/responder"
)

// HandleAdminGetHealth is the detailed health view behind admin auth. The
// public /healthcheck says only "ok"; this adds DB status and go-monitor's
// counters, so lost or spooled events stay visible from outside: a service
// whose events stop arriving cannot report it through Monitor.
func HandleAdminGetHealth(w http.ResponseWriter, r *http.Request) {
	status, dbStatus := "ok", "ok"
	if err := db.DB.Ping(); err != nil {
		status, dbStatus = "degraded", "error"
	}
	responder.New(w, map[string]any{"status": status, "db": dbStatus, "telemetry": monitor.Stats()})
}
