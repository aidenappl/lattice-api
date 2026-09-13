package routers

import (
	"log"
	"net/http"
	"strings"
	"time"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
)

// logAudit records an audit log entry asynchronously. It extracts the user
// and IP address from the request context. Errors are logged but never fail
// the request.
func logAudit(r *http.Request, action, resourceType string, resourceID *int, details *string) {
	user, _ := middleware.GetUserFromContext(r.Context())
	var userID *int
	if user != nil {
		userID = &user.ID
	}
	ip := r.RemoteAddr

	// Every audited mutation also reaches Monitor, on the request's own ids,
	// so the trail survives the audit table's 180-day retention and can be read
	// beside the failures around it. Details are left out: they are free text.
	fields := map[string]any{"resource_type": resourceType, "action": action}
	if resourceID != nil {
		fields["resource_id"] = *resourceID
	}
	monitor.Info(r.Context(), resourceType+"."+strings.ReplaceAll(action, " ", "_")+".success", fields)

	go func() {
		defer logger.Recover("audit", logger.F{"action": action, "resource_type": resourceType})
		req := query.CreateAuditLogRequest{
			UserID:       userID,
			Action:       action,
			ResourceType: resourceType,
			ResourceID:   resourceID,
			Details:      details,
			IPAddress:    &ip,
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			err = query.CreateAuditLog(db.DB, req)
			if err == nil {
				return
			}
			log.Printf("audit log error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(500 * time.Millisecond)
		}
		failed := logger.F{"action": action, "resource_type": resourceType, "error": err}
		if resourceID != nil {
			failed["resource_id"] = *resourceID
		}
		logger.Error("audit", "audit log write failed after 3 attempts", failed)
	}()
}

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// parseWorkerLabels parses comma-separated key=value labels from a worker's labels field.
func parseWorkerLabels(raw *string) map[string]string {
	labels := make(map[string]string)
	if raw == nil || *raw == "" {
		return labels
	}
	for _, pair := range strings.Split(*raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 {
			labels[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return labels
}
