package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
)

const (
	UserContextKey   contextKey = "user"
	latticeTokenName            = "lattice-access-token"
)

// GetUserFromContext returns the authenticated user injected by DualAuthMiddleware.
func GetUserFromContext(ctx context.Context) (*structs.User, bool) {
	user, ok := ctx.Value(UserContextKey).(*structs.User)
	return user, ok
}

// DualAuthMiddleware checks authentication from either:
// 1. Lattice-issued JWT (local users) via Authorization: Bearer header
// 2. Lattice-issued JWT from lattice-access-token cookie
// SSO users receive Lattice JWTs via the SSO callback, so they authenticate
// the same way as local users after login.
// On success, injects *structs.User into the request context.
func DualAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try Lattice JWT from Authorization header
		if token := extractBearerToken(r); token != "" {
			if user := validateLatticeToken(token); user != nil {
				ctx := context.WithValue(r.Context(), UserContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// Try Lattice JWT from cookie
		if cookie, err := r.Cookie(latticeTokenName); err == nil && cookie.Value != "" {
			if user := validateLatticeToken(cookie.Value); user != nil {
				ctx := context.WithValue(r.Context(), UserContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		responder.SendError(w, http.StatusUnauthorized, "authentication required")
	})
}

// RejectPending blocks users with role "pending" from accessing protected routes.
// Pending users can still access /auth/self to check their status.
func RejectPending(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
		if ok && user != nil && user.Role == "pending" {
			responder.SendErrorWithCode(w, http.StatusForbidden, "your account is pending admin approval", 4004)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin wraps a handler to require the authenticated user has admin role.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
		if !ok || user == nil {
			responder.SendError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if user.Role != "admin" {
			responder.SendError(w, http.StatusForbidden, "admin access required")
			return
		}
		next(w, r)
	}
}

// RequireEditor wraps a handler to require the authenticated user has admin or editor role.
func RequireEditor(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
		if !ok || user == nil {
			responder.SendError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if user.Role != "admin" && user.Role != "editor" {
			responder.SendError(w, http.StatusForbidden, "editor access required")
			return
		}
		next(w, r)
	}
}

// WorkerTokenAuth validates a worker API token from the X-Worker-Token header.
// Query parameter auth is only allowed for WebSocket upgrade requests because
// WebSocket clients cannot set custom headers during the HTTP upgrade handshake.
// Returns the worker_id on success.
func WorkerTokenAuth(r *http.Request) (int, bool) {
	token := r.Header.Get("X-Worker-Token")
	// Allow query param only for WebSocket upgrades (clients can't set headers)
	if token == "" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return 0, false
	}

	hash := tools.HashToken(token)
	wt, err := query.GetWorkerTokenByHash(db.DB, hash)
	if err != nil || wt == nil || !wt.Active {
		return 0, false
	}

	// Update last_used_at
	_ = query.TouchWorkerToken(db.DB, wt.ID)

	return wt.WorkerID, true
}

func validateLatticeToken(tokenStr string) *structs.User {
	claims, err := jwt.ValidateToken(tokenStr)
	if err != nil || claims.Type != "access" {
		return nil
	}

	user, err := query.GetUserByID(db.DB, claims.UserID)
	if err != nil || user == nil || !user.Active {
		return nil
	}

	// Reject tokens issued before the revocation timestamp
	if user.TokensRevokedAt != nil && claims.IssuedAt != nil {
		if claims.IssuedAt.Time.Before(*user.TokensRevokedAt) {
			return nil
		}
	}

	return user
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}
