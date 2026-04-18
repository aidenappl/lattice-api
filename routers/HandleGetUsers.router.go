package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetUsers(w http.ResponseWriter, r *http.Request) {
	req := query.ListUsersRequest{}

	if v := r.URL.Query().Get("active"); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			req.Active = &b
		}
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

	users, err := query.ListUsers(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list users")
		return
	}

	responder.New(w, users, "users retrieved")
}
