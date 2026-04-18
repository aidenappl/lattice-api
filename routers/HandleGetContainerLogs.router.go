package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetContainerLogs(w http.ResponseWriter, r *http.Request) {
	req := query.ListLogsRequest{}

	vars := mux.Vars(r)
	if v, ok := vars["id"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			req.ContainerID = &n
			// Look up container name so we can also match logs stored by name
			if c, err := query.GetContainerByID(db.DB, n); err == nil {
				req.ContainerName = &c.Name
			}
		}
	}

	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerID = &n
		}
	}
	if v := r.URL.Query().Get("stream"); v != "" {
		req.Stream = &v
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

	logs, err := query.ListContainerLogs(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list container logs")
		return
	}

	responder.New(w, logs)
}
