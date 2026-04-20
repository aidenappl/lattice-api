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

func HandleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if user.ID == id {
		responder.SendError(w, http.StatusBadRequest, "cannot deactivate yourself")
		return
	}

	if err := query.DeleteUser(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete user")
		return
	}

	logAudit(r, "delete", "user", intPtr(id), nil)
	responder.New(w, nil, "user deleted")
}
