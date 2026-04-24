package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleGetWorkerContainerStats(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	metrics, err := query.GetLatestWorkerContainerMetrics(db.DB, workerID)
	if err != nil {
		responder.QueryError(w, err, "failed to get worker container stats")
		return
	}

	responder.New(w, metrics, "worker container stats retrieved")
}
