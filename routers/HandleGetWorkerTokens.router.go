package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetWorkerTokens(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	tokens, err := query.ListWorkerTokens(db.DB, workerID)
	if err != nil {
		responder.QueryError(w, err, "failed to list worker tokens")
		return
	}

	responder.New(w, tokens, "worker tokens retrieved")
}
