package routers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
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

	// Look up specifically the local account for this email
	user, err := query.GetUserByEmailAndAuthType(db.DB, body.Email, "local")
	if err != nil || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if user.PasswordHash == nil {
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

	secure := env.Environment == "production"
	domain := env.CookieDomain

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-access-token",
		Value:    accessToken,
		Path:     "/",
		Domain:   domain,
		Expires:  accessExpiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-refresh-token",
		Value:    refreshToken,
		Path:     "/",
		Domain:   domain,
		Expires:  refreshExpiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "logged_in",
		Value:    "1",
		Path:     "/",
		Domain:   domain,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	responder.New(w, map[string]any{
		"user":         user,
		"access_token": accessToken,
		"expires_at":   accessExpiry,
	}, "login successful")
}
