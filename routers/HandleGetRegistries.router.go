package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetRegistries(w http.ResponseWriter, r *http.Request) {
	registries, err := query.ListRegistries(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list registries")
		return
	}

	responder.New(w, registries, "registries retrieved")
}
