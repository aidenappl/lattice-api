package routers

import (
	"net/http"
	"time"

	"github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/hostdisk"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/telemetry"
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
	// telemetry_delivery.stalled means events are waiting and none has been
	// delivered for telemetry.STALL_AFTER: Monitor is not hearing from this
	// process, so this is the only place it can be seen.
	delivery := telemetry.Delivery()
	if delivery.Stalled {
		status = "degraded"
	}
	// The control-plane host's free space (APL-121): root fs, the compose
	// dir and MariaDB's data dir, sampled every minute by hostdisk.
	disk := hostdisk.Default.Latest(time.Now())
	for _, s := range disk {
		if s.Low {
			status = "degraded"
		}
	}
	responder.New(w, map[string]any{
		"status":             status,
		"db":                 dbStatus,
		"telemetry":          monitor.Stats(),
		"telemetry_delivery": delivery,
		"control_plane_disk": disk,
	})
}
