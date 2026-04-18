package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}

	logs, err := query.ListDeploymentLogs(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get deployment logs")
		return
	}

	responder.New(w, logs, "deployment logs retrieved")
}
