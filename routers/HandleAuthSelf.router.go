package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleAuthSelf(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	responder.New(w, user, "user retrieved")
}
