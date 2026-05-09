package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleDeleteApiToken(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid token id")
		return
	}

	if user.Role != "admin" {
		tokens, err := query.ListApiTokens(db.DB, &user.ID)
		if err != nil {
			responder.QueryError(w, err, "failed to verify token ownership")
			return
		}
		owned := false
		for _, t := range *tokens {
			if t.ID == id {
				owned = true
				break
			}
		}
		if !owned {
			responder.SendError(w, http.StatusForbidden, "you can only delete your own api tokens")
			return
		}
	}

	if err := query.DeleteApiToken(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete api token")
		return
	}

	logAudit(r, "delete", "api_token", intPtr(id), nil)
	responder.New(w, nil, "api token deleted")
}
