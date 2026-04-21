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
// It sets a lattice-csrf cookie on every response and validates that mutating requests
// include a matching X-CSRF-Token header.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CSRF validation for safe methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			setCSRFCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		// Skip CSRF for Bearer token auth (API clients are not vulnerable to CSRF)
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip CSRF for exempt paths
		path := r.URL.Path
		if path == "/auth/login" || path == "/auth/refresh" || path == "/ws/worker" {
			setCSRFCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		// Validate CSRF token: header must match cookie
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			http.Error(w, `{"error":"missing CSRF cookie"}`, http.StatusForbidden)
			return
		}

		headerToken := r.Header.Get(csrfHeaderName)
		if headerToken == "" {
			http.Error(w, `{"error":"missing CSRF token header"}`, http.StatusForbidden)
			return
		}

		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) != 1 {
			http.Error(w, `{"error":"CSRF token mismatch"}`, http.StatusForbidden)
			return
		}

		setCSRFCookie(w)
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
