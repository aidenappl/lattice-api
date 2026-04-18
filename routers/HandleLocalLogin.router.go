package routers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleLocalLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Email == "" {
		responder.MissingBodyFields(w, "email")
		return
	}
	if body.Password == "" {
		responder.MissingBodyFields(w, "password")
		return
	}

	user, err := query.GetUserByEmail(db.DB, body.Email)
	if err != nil {
		responder.SendError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if user.AuthType != "local" || user.PasswordHash == nil {
		responder.SendError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !user.Active {
		responder.SendError(w, http.StatusForbidden, "account is disabled")
		return
	}

	if !tools.CheckPassword(*user.PasswordHash, body.Password) {
		responder.SendError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	accessToken, accessExpiry, err := jwt.NewAccessToken(user.ID)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate access token", err)
		return
	}

	refreshToken, refreshExpiry, err := jwt.NewRefreshToken(user.ID)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate refresh token", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-access-token",
		Value:    accessToken,
		Path:     "/",
		Expires:  accessExpiry,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-refresh-token",
		Value:    refreshToken,
		Path:     "/",
		Expires:  refreshExpiry,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "logged_in",
		Value:    "1",
		Path:     "/",
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	responder.New(w, map[string]any{
		"user":         user,
		"access_token": accessToken,
		"expires_at":   accessExpiry,
	}, "login successful")
}
