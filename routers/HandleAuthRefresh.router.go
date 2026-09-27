package routers

import (
	"errors"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("lattice-refresh-token")
	if err != nil || cookie.Value == "" {
		logger.DebugCtx(r.Context(), "auth", "refresh rejected: no refresh token cookie")
		responder.SendError(w, http.StatusUnauthorized, "no refresh token provided")
		return
	}

	claims, err := jwt.ValidateToken(cookie.Value)
	if err != nil || claims == nil || claims.Type != "refresh" {
		claimType := ""
		if claims != nil {
			claimType = claims.Type
		}
		fields := logger.F{"token_type": claimType}
		if err != nil {
			fields["error"] = err
		}
		logger.InfoCtx(r.Context(), "auth", "refresh rejected: invalid refresh token", fields)
		responder.SendError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	user, err := query.GetUserByID(db.DB, claims.UserID)
	if err != nil || user == nil || !user.Active {
		if err != nil && !errors.Is(err, query.ErrNotFound) {
			// A database failure, not a bad token: the 401 below hides it from
			// the client, so this is its only record.
			logger.ErrorCtx(r.Context(), "auth", "refresh user lookup failed", logger.F{"user_id": claims.UserID, "error": err})
		} else {
			logger.InfoCtx(r.Context(), "auth", "refresh rejected: user missing or inactive", logger.F{"user_id": claims.UserID})
		}
		responder.SendError(w, http.StatusUnauthorized, "user not found or inactive")
		return
	}

	// Reject refresh tokens issued at or before the revocation timestamp.
	//
	// ⚠️ `!After` AND `nil iat` MUST MATCH validateLatticeToken IN
	// middleware/auth.go — the two comparisons had diverged.
	//
	// This used `Before` and required a non-nil iat, so a token minted in the
	// SAME SECOND as the revocation passed here while the middleware rejected
	// it, and a token with no iat passed here while the middleware treated it as
	// revoked. Refresh is precisely where that gap matters: the middleware
	// rejects the access token, the client refreshes, and a weaker check here
	// hands back a brand-new pair whose iat is after the stamp — undoing the
	// revocation entirely.
	//
	// openbucket-api had the same class of hole with no check at all; confirmed
	// in production 2026-08-10 when a revoked session refreshed itself back in
	// one second after back-channel logout reported "ended 1 session(s)".
	if user.TokensRevokedAt != nil {
		if claims.IssuedAt == nil || !claims.IssuedAt.Time.After(*user.TokensRevokedAt) {
			// ⚠️ nil-safe: the condition above ENTERS this branch when IssuedAt is
			// nil, so dereferencing it here would panic on exactly the token this
			// check exists to reject.
			issued := "nil"
			if claims.IssuedAt != nil {
				issued = claims.IssuedAt.Time.String()
			}
			logger.WarnCtx(r.Context(), "auth", "refresh rejected: token revoked", logger.F{
				"user_id":    user.ID,
				"issued_at":  issued,
				"revoked_at": user.TokensRevokedAt.UTC().Format(time.RFC3339),
			})
			responder.SendError(w, http.StatusUnauthorized, "token has been revoked")
			return
		}
	}

	logger.DebugCtx(r.Context(), "auth", "refresh accepted", logger.F{"user_id": user.ID})

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
