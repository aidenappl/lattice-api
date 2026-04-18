package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleDeleteContainer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	if err := query.DeleteContainer(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete container")
		return
	}

	responder.New(w, nil, "container deleted")
}
