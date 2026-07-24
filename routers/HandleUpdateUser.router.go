package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		Name   *string `json:"name"`
		Role   *string `json:"role"`
		Active *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	if body.Role != nil {
		validRoles := map[string]bool{"admin": true, "editor": true, "viewer": true, "pending": true}
		if !validRoles[*body.Role] {
			responder.SendError(w, http.StatusBadRequest, "role must be admin, editor, viewer, or pending")
			return
		}
	}

	// Load the target so we can 404 on a missing user and evaluate the
	// admin-lockout guards against its current state.
	target, err := query.GetUserByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to load user")
		return
	}

	// Determine the user's role/active after this update.
	newRole := target.Role
	if body.Role != nil {
		newRole = *body.Role
	}
	newActive := target.Active
	if body.Active != nil {
		newActive = *body.Active
	}
	losingAdmin := target.Role == "admin" && (newRole != "admin" || !newActive)

	// Self-demotion guard: an admin can't strip their own admin access (would
	// immediately lock themselves out mid-request).
	if acting, ok := middleware.GetUserFromContext(r.Context()); ok && acting != nil && acting.ID == id && losingAdmin {
		responder.SendError(w, http.StatusBadRequest, "you cannot remove your own admin access")
		return
	}

	// Last-admin guard: never demote/deactivate the final active admin.
	if losingAdmin {
		admins, err := query.CountActiveAdmins(db.DB)
		if err != nil {
			responder.QueryError(w, err, "failed to verify admin count")
			return
		}
		if admins <= 1 {
			responder.SendError(w, http.StatusBadRequest, "cannot remove the last active admin")
			return
		}
	}

	user, err := query.UpdateUser(db.DB, id, query.UpdateUserRequest{
		Name:   body.Name,
		Role:   body.Role,
		Active: body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update user")
		return
	}

	logAudit(r, "update", "user", intPtr(id), nil)
	responder.New(w, user, "user updated")
}
