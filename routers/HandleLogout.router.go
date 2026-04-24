package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
)

func HandleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke all tokens for this user so stolen refresh tokens can't be reused
	if user, ok := middleware.GetUserFromContext(r.Context()); ok && user != nil {
		_ = query.RevokeUserTokens(db.DB, user.ID)
	}

	domain := env.CookieDomain

	// Clear auth cookies by setting them expired
	for _, name := range []string{"lattice-access-token", "lattice-refresh-token", "logged_in"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   domain,
			MaxAge:   -1,
			HttpOnly: name != "logged_in",
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
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true,"message":"logged out"}`))
}
