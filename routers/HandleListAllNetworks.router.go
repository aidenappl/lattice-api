package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleListAllNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := query.ListAllNetworks(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list networks")
		return
	}

	responder.New(w, networks, "networks retrieved")
}
