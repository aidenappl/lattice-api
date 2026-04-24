package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/env"
)

const csrfCookieName = "lattice-csrf"
const csrfHeaderName = "X-CSRF-Token"

// CSRFMiddleware implements the double-submit cookie pattern for CSRF protection.
// It sets a lattice-csrf cookie on first visit (not rotated per-request) and validates
// that mutating requests include a matching X-CSRF-Token header.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ensure CSRF cookie exists (set on first visit, not rotated per-request)
		if _, err := r.Cookie(csrfCookieName); err != nil {
			setCSRFCookie(w)
		}

		// Skip validation for safe methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Skip for Bearer token auth
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip exempt paths
		path := r.URL.Path
		if path == "/auth/login" || path == "/auth/refresh" || path == "/ws/worker" || strings.HasPrefix(path, "/api/deploy/") || path == "/auth/sso/callback" {
			next.ServeHTTP(w, r)
			return
		}

		// Validate
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"error":"csrf_error","error_message":"missing CSRF cookie","error_code":4030}`))
			return
		}
		headerToken := r.Header.Get(csrfHeaderName)
		if headerToken == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"error":"csrf_error","error_message":"CSRF token mismatch","error_code":4031}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// setCSRFCookie generates a new CSRF token and sets it as a cookie on the response.
func setCSRFCookie(w http.ResponseWriter) {
	token := generateCSRFToken()
	secure := env.Environment == "production"

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		Domain:   env.CookieDomain,
		HttpOnly: false, // JavaScript needs to read this
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// generateCSRFToken returns a cryptographically random 32-byte hex-encoded token.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
