package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/env"
)

func HandleLogout(w http.ResponseWriter, r *http.Request) {
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
