package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleListApiTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var userID *int
	if user.Role != "admin" {
		userID = &user.ID
	}

	tokens, err := query.ListApiTokens(db.DB, userID)
	if err != nil {
		responder.QueryError(w, err, "failed to list api tokens")
		return
	}

	responder.New(w, tokens, "api tokens retrieved")
}
