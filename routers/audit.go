package routers

import (
	"log"
	"net/http"

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
		err := query.CreateAuditLog(db.DB, query.CreateAuditLogRequest{
			UserID:       userID,
			Action:       action,
			ResourceType: resourceType,
			ResourceID:   resourceID,
			Details:      details,
			IPAddress:    &ip,
		})
		if err != nil {
			log.Printf("audit log error: %v", err)
		}
	}()
}

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }
