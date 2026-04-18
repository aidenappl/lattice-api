package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleRollbackDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}

	if err := query.UpdateDeploymentStatus(db.DB, id, "rolled_back"); err != nil {
		responder.QueryError(w, err, "failed to rollback deployment")
		return
	}

	deployment, err := query.GetDeploymentByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to retrieve deployment")
		return
	}

	responder.New(w, deployment, "deployment rolled back")
}
