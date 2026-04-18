package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// HandleGetContainer returns a single container by ID.
func HandleGetContainer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	container, err := query.GetContainerByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get container")
		return
	}

	responder.New(w, container, "container retrieved")
}
