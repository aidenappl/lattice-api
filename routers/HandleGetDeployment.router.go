package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}

	deployment, err := query.GetDeploymentByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	responder.New(w, deployment, "deployment retrieved")
}
