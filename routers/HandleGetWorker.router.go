package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetWorker(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	worker, err := query.GetWorkerByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	responder.New(w, worker, "worker retrieved")
}
