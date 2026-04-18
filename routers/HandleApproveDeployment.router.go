package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleApproveDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}

	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if err := query.ApproveDeployment(db.DB, id, user.ID); err != nil {
		responder.QueryError(w, err, "failed to approve deployment")
		return
	}

	deployment, err := query.GetDeploymentByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to retrieve deployment")
		return
	}

	responder.New(w, deployment, "deployment approved")
}
