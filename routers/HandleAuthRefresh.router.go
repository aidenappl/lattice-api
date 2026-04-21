package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("lattice-refresh-token")
	if err != nil || cookie.Value == "" {
		responder.SendError(w, http.StatusUnauthorized, "no refresh token provided")
		return
	}

	userID, err := jwt.ValidateRefreshToken(cookie.Value)
	if err != nil {
		responder.SendError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	user, err := query.GetUserByID(db.DB, userID)
	if err != nil || user == nil || !user.Active {
		responder.SendError(w, http.StatusUnauthorized, "user not found or inactive")
		return
	}

	accessToken, accessExpiry, err := jwt.NewAccessToken(user.ID)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate access token", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-access-token",
		Value:    accessToken,
		Path:     "/",
		Domain:   env.CookieDomain,
		Expires:  accessExpiry,
		HttpOnly: true,
		Secure:   env.Environment == "production",
		SameSite: http.SameSiteLaxMode,
	})

	responder.New(w, map[string]any{
		"token":      accessToken,
		"expires_at": accessExpiry,
	}, "token refreshed")
}
