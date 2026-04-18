package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetAuditLog(w http.ResponseWriter, r *http.Request) {
	req := query.ListAuditLogRequest{}

	if v := r.URL.Query().Get("user_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.UserID = &n
		}
	}
	if v := r.URL.Query().Get("action"); v != "" {
		req.Action = &v
	}
	if v := r.URL.Query().Get("resource_type"); v != "" {
		req.ResourceType = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Offset = n
		}
	}

	entries, err := query.ListAuditLog(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list audit log")
		return
	}

	responder.New(w, entries, "audit log retrieved")
}
