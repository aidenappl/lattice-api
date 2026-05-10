package routers

import (
	"log"
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
		log.Printf("auth/refresh: no refresh token cookie (err=%v)", err)
		responder.SendError(w, http.StatusUnauthorized, "no refresh token provided")
		return
	}

	claims, err := jwt.ValidateToken(cookie.Value)
	if err != nil || claims == nil || claims.Type != "refresh" {
		claimType := ""
		if claims != nil {
			claimType = claims.Type
		}
		log.Printf("auth/refresh: invalid refresh token (err=%v, type=%s)", err, claimType)
		responder.SendError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	user, err := query.GetUserByID(db.DB, claims.UserID)
	if err != nil || user == nil || !user.Active {
		log.Printf("auth/refresh: user lookup failed (user_id=%d, err=%v, active=%v)", claims.UserID, err, user != nil && user.Active)
		responder.SendError(w, http.StatusUnauthorized, "user not found or inactive")
		return
	}

	// Reject refresh tokens issued before the revocation timestamp
	if user.TokensRevokedAt != nil && claims.IssuedAt != nil {
		if claims.IssuedAt.Time.Before(*user.TokensRevokedAt) {
			log.Printf("auth/refresh: token revoked (user_id=%d, issued=%v, revoked=%v)", user.ID, claims.IssuedAt.Time, *user.TokensRevokedAt)
			responder.SendError(w, http.StatusUnauthorized, "token has been revoked")
			return
		}
	}

	log.Printf("auth/refresh: success (user_id=%d)", user.ID)

	accessToken, accessExpiry, err := jwt.NewAccessToken(user.ID)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate access token", err)
		return
	}

	// Reissue refresh token (sliding window) — extends the 7-day session
	// on each refresh so active users never hit refresh token expiry
	refreshToken, refreshExpiry, err := jwt.NewRefreshToken(user.ID)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate refresh token", err)
		return
	}

	secure := env.Environment == "production"

	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-access-token",
		Value:    accessToken,
		Path:     "/",
		Domain:   env.CookieDomain,
		Expires:  accessExpiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "lattice-refresh-token",
		Value:    refreshToken,
		Path:     "/",
		Domain:   env.CookieDomain,
		Expires:  refreshExpiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	responder.New(w, map[string]any{
		"token":      accessToken,
		"expires_at": accessExpiry,
	}, "token refreshed")
}
