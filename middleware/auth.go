package middleware

import (
	"context"
	"net/http"
	"strings"

	forta "github.com/aidenappl/go-forta"
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
// 1. Lattice-issued JWT (local users) via Authorization: Bearer header or lattice-access-token cookie
// 2. Forta access token (OAuth users) via forta-access-token cookie
// On success, injects *structs.User into the request context.
func DualAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try Lattice JWT first (from Authorization header)
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

		// Try Forta authentication
		if fortaUser, err := forta.FetchCurrentUser(r); err == nil && fortaUser != nil {
			user, err := query.GetUserByFortaID(db.DB, fortaUser.ID)
			if err != nil || user == nil {
				// Auto-create OAuth user on first login
				name := ""
				if fortaUser.Name != nil {
					name = *fortaUser.Name
				}
				user, err = query.CreateUser(db.DB, query.CreateUserRequest{
					Email:    fortaUser.Email,
					Name:     &name,
					AuthType: "oauth",
					FortaID:  &fortaUser.ID,
					Role:     "viewer",
				})
				if err != nil {
					responder.SendError(w, http.StatusInternalServerError, "failed to create user from forta", err)
					return
				}
			}
			if !user.Active {
				responder.SendError(w, http.StatusForbidden, "account is disabled")
				return
			}
			ctx := context.WithValue(r.Context(), UserContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		responder.SendError(w, http.StatusUnauthorized, "authentication required")
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

// WorkerTokenAuth validates a worker API token from the query string or header.
// Returns the worker_id on success, or writes an error response and returns 0.
func WorkerTokenAuth(r *http.Request) (int, bool) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Worker-Token")
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
	userID, err := jwt.ValidateAccessToken(tokenStr)
	if err != nil {
		return nil
	}

	user, err := query.GetUserByID(db.DB, userID)
	if err != nil || user == nil || !user.Active {
		return nil
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
