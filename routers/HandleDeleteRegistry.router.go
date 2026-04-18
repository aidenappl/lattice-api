package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	if err := query.DeleteRegistry(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete registry")
		return
	}

	responder.New(w, nil, "registry deleted")
}
