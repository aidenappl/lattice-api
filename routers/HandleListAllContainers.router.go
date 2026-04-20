package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

// HandleListAllContainers returns all active containers, optionally filtered by
// stack_id or worker_id query params.
func HandleListAllContainers(w http.ResponseWriter, r *http.Request) {
	var req query.ListAllContainersRequest

	if v := r.URL.Query().Get("stack_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.StackID = &n
		}
	}
	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerID = &n
		}
	}
	if v := r.URL.Query().Get("name"); v != "" {
		req.Name = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		req.Status = &v
	}

	containers, err := query.ListAllContainers(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	responder.New(w, containers, "containers retrieved")
}
