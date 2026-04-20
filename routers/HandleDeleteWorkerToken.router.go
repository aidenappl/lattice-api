package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleDeleteWorkerToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid token id")
		return
	}

	if err := query.DeleteWorkerToken(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete worker token")
		return
	}

	logAudit(r, "delete", "worker_token", intPtr(id), nil)
	responder.New(w, nil, "worker token deleted")
}
