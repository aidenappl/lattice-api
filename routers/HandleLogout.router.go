package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

// Swappable in tests.
var (
	revokeUserTokens = query.RevokeUserTokens
	deleteSSOSession = query.DeleteSSOSession
)

func HandleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke all tokens for this user so stolen refresh tokens can't be reused.
	//
	// The cookies are cleared whatever happens: callers navigate to /login
	// regardless of the result, so keeping them on a failed revoke would leave
	// the person signed in (an idle-timeout logout included). A failed revoke
	// is still reported as a 500, which records it as one error-level event.
	var revokeErr error
	if user, ok := middleware.GetUserFromContext(r.Context()); ok && user != nil {
		if err := revokeUserTokens(db.DB, user.ID); err != nil {
			revokeErr = err
		} else if user.AuthType == "sso" {
			// If this is an SSO user, drop the persisted IDP tokens too.
			if err := deleteSSOSession(db.DB, int64(user.ID)); err != nil {
				revokeErr = err
			}
		}
	}

	domain := env.CookieDomain

	// Clear auth cookies by setting them expired
	for _, name := range []string{"lattice-access-token", "lattice-refresh-token", "lattice-logged-in"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   domain,
			MaxAge:   -1,
			HttpOnly: name != "lattice-logged-in",
			Secure:   env.Environment == "production",
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Also clear the CSRF cookie
	http.SetCookie(w, &http.Cookie{
		Name:   "lattice-csrf",
		Value:  "",
		Path:   "/",
		Domain: domain,
		MaxAge: -1,
	})

	if revokeErr != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to revoke session", revokeErr)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true,"message":"logged out"}`))
}
