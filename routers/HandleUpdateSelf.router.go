package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

// HandleUpdateSelf allows authenticated users to update their own profile.
// PUT /auth/self
// Accepts: { name, password, current_password }
// Users can change their name and password (if local auth), but NOT their role or email.
func HandleUpdateSelf(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		Name            *string `json:"name"`
		CurrentPassword *string `json:"current_password"`
		NewPassword     *string `json:"new_password"`
		ProfileImageURL *string `json:"profile_image_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Update name if provided
	if body.Name != nil {
		_, err := query.UpdateUser(db.DB, user.ID, query.UpdateUserRequest{Name: body.Name})
		if err != nil {
			responder.QueryError(w, err, "failed to update name")
			return
		}
	}

	// Update profile image if provided
	if body.ProfileImageURL != nil {
		_, err := query.UpdateUser(db.DB, user.ID, query.UpdateUserRequest{ProfileImageURL: body.ProfileImageURL})
		if err != nil {
			responder.QueryError(w, err, "failed to update profile image")
			return
		}
	}

	// Change password (local auth only)
	if body.NewPassword != nil && *body.NewPassword != "" {
		if user.AuthType != "local" {
			responder.SendError(w, http.StatusBadRequest, "password change only available for local auth accounts")
			return
		}
		if body.CurrentPassword == nil || *body.CurrentPassword == "" {
			responder.SendError(w, http.StatusBadRequest, "current password is required")
			return
		}
		// Verify current password
		if user.PasswordHash == nil || !tools.CheckPassword(*user.PasswordHash, *body.CurrentPassword) {
			responder.SendError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		// Validate new password
		if err := tools.ValidatePassword(*body.NewPassword); err != nil {
			responder.SendError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := tools.HashPassword(*body.NewPassword)
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		_, err = query.UpdateUser(db.DB, user.ID, query.UpdateUserRequest{PasswordHash: &hash})
		if err != nil {
			responder.QueryError(w, err, "failed to update password")
			return
		}
	}

	// Return updated user
	updated, err := query.GetUserByID(db.DB, user.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to get updated user")
		return
	}

	logAudit(r, "update_self", "user", intPtr(user.ID), nil)
	responder.New(w, updated, "profile updated")
}
