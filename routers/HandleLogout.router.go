package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/sso"
)

// Swappable in tests.
var revokeUserTokens = query.RevokeUserTokens

func HandleLogout(w http.ResponseWriter, r *http.Request) {
	// An SSO session ends here and, through RP-initiated logout, at the
	// identity provider too — which back-channels every other application.
	// The frontend navigates to redirect_url to complete that.
	redirectURL := ""
	if c, err := r.Cookie(sso.SessionCookie); err == nil && c.Value != "" {
		redirectURL = sso.Manager().Logout(w, r)
	}

	var revokeErr error
	if user, ok := middleware.GetUserFromContext(r.Context()); ok && user != nil {
		revokeErr = revokeUserTokens(db.DB, user.ID)
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

	responder.New(w, map[string]string{"redirect_url": redirectURL}, "logged out")
}
