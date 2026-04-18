package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetStacks(w http.ResponseWriter, r *http.Request) {
	req := query.ListStacksRequest{}

	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerID = &n
		}
	}
	if v := r.URL.Query().Get("status"); v != "" {
		req.Status = &v
	}
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

	stacks, err := query.ListStacks(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list stacks")
		return
	}

	responder.New(w, stacks, "stacks retrieved")
}
