package routers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
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

	go func() {
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
		log.Printf("audit log failed after 3 attempts: %v", err)
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
