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
	var stackID *int
	var workerID *int

	if v := r.URL.Query().Get("stack_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			stackID = &n
		}
	}
	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			workerID = &n
		}
	}

	containers, err := query.ListAllContainers(db.DB, stackID, workerID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	responder.New(w, containers, "containers retrieved")
}
