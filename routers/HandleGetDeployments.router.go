package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetDeployments(w http.ResponseWriter, r *http.Request) {
	req := query.ListDeploymentsRequest{}

	if v := r.URL.Query().Get("stack_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.StackID = &n
		}
	}
	if v := r.URL.Query().Get("status"); v != "" {
		req.Status = &v
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

	deployments, err := query.ListDeployments(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list deployments")
		return
	}

	responder.New(w, deployments, "deployments retrieved")
}
