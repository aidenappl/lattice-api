package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
)

// ssoCheckpointTTL controls how often the auth middleware re-validates an
// SSO user's grant against the IDP. Shorter = faster revocation propagation,
// more network calls. 5 min matches the IDP's recommended access token TTL.
const ssoCheckpointTTL = 5 * time.Minute

// ssoCheckpointGrace bounds how long the checkpoint may keep failing OPEN. If
// the IDP grant has not been positively re-confirmed (last_checked_at) within
// this window, the checkpoint fails CLOSED — the request is denied rather than
// allowed on an unverified grant. It does not revoke tokens, so once the IDP is
// reachable again and reports active, the user is allowed without re-login.
const ssoCheckpointGrace = 30 * time.Minute

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
func DualAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearerToken := extractBearerToken(r)

		// Try Lattice JWT from Authorization header
		if bearerToken != "" {
			if user := validateLatticeToken(bearerToken); user != nil {
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

		// Try API token (long-lived) from Authorization header
		if bearerToken != "" {
			if user, apiToken := validateApiToken(bearerToken); user != nil {
				if !apiTokenScopeAllows(apiToken.Scopes, r.Method) {
					responder.SendError(w, http.StatusForbidden, "api token scope does not permit this operation")
					return
				}
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

// API token scope model. A token's Scopes column is a comma-separated list of
// scope names. Recognized values:
//
//	read  — safe (GET/HEAD/OPTIONS) requests only
//	write — read + mutating requests (still subject to the user's RBAC role)
//	admin — same request surface as write (admin-only routes are still gated by
//	        RequireAdmin against the owning user's role)
//
// A nil/empty scope means the token is UNRESTRICTED — this is the backward-
// compatible default for tokens minted before scope enforcement existed (the
// dashboard and MCP never sent a scopes field, so every existing token is nil).
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// apiTokenScopeAllows reports whether an API token carrying the given scopes
// string may perform a request with the given HTTP method. A nil/empty scope is
// unrestricted; a scope granting write/admin allows all methods; anything else
// (read-only or unknown-but-non-write) is limited to safe methods.
func apiTokenScopeAllows(scopes *string, method string) bool {
	if scopes == nil {
		return true // unrestricted (legacy token)
	}
	s := strings.TrimSpace(*scopes)
	if s == "" {
		return true
	}
	for _, p := range strings.Split(s, ",") {
		switch strings.TrimSpace(strings.ToLower(p)) {
		case ScopeWrite, ScopeAdmin:
			return true
		}
	}
	return isSafeMethod(method)
}

// NormalizeApiTokenScopes validates and canonicalizes a user-supplied scopes
// string. It returns the normalized value (lowercased, trimmed, de-duplicated,
// comma-joined) and false if any token is not a recognized scope. A nil input
// (no scopes field) is accepted as-is (unrestricted).
func NormalizeApiTokenScopes(scopes *string) (*string, bool) {
	if scopes == nil {
		return nil, true
	}
	raw := strings.TrimSpace(*scopes)
	if raw == "" {
		return nil, true
	}
	seen := make(map[string]struct{})
	var out []string
	for _, p := range strings.Split(raw, ",") {
		v := strings.TrimSpace(strings.ToLower(p))
		if v == "" {
			continue
		}
		switch v {
		case ScopeRead, ScopeWrite, ScopeAdmin:
		default:
			return nil, false
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, true
	}
	joined := strings.Join(out, ",")
	return &joined, true
}

func validateApiToken(tokenStr string) (*structs.User, *structs.ApiToken) {
	hash := tools.HashToken(tokenStr)
	apiToken, err := query.GetApiTokenByHash(db.DB, hash)
	if err != nil || apiToken == nil || !apiToken.Active {
		return nil, nil
	}

	user, err := query.GetUserByID(db.DB, apiToken.UserID)
	if err != nil || user == nil || !user.Active {
		return nil, nil
	}

	_ = query.TouchApiToken(db.DB, apiToken.ID)

	return user, apiToken
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

	// Reject tokens issued at or before the revocation timestamp. Using !After
	// (rather than Before) so a token minted in the same second as the revocation
	// is also rejected; a nil iat is treated as revoked (ValidateToken already
	// rejects nil iat, this is belt-and-suspenders).
	if user.TokensRevokedAt != nil {
		if claims.IssuedAt == nil || !claims.IssuedAt.Time.After(*user.TokensRevokedAt) {
			return nil
		}
	}

	if user.AuthType == "sso" && !checkpointSSOGrant(int64(user.ID)) {
		return nil
	}

	return user
}

// checkpointSSOGrant re-validates the user's grant against the IDP on a TTL.
// Returns true if the grant is still active (or the check is skipped because
// it ran recently). Returns false if the IDP reports active=false — the
// sso_sessions row is deleted and the caller MUST 401.
//
// Network/decrypt errors fail-open (return true) ONLY within a bounded grace
// window (ssoCheckpointGrace) measured from the last positive confirmation
// (last_checked_at) — a transient IDP outage shouldn't log users out. Once the
// grant has gone unconfirmed past that window, the checkpoint fails CLOSED
// (returns false) rather than trusting an unverified grant indefinitely. It does
// not revoke tokens in that case, so a recovered IDP re-admits the user without
// forcing a re-login.
func checkpointSSOGrant(userID int64) bool {
	sess, err := query.GetSSOSession(db.DB, userID)
	if err != nil {
		logger.Warn("auth", "checkpoint: db lookup failed", logger.F{"user_id": userID, "error": err})
		return true
	}
	if sess == nil {
		// SSO user with no stored IDP tokens — pre-checkpoint legacy state.
		// Allow; the next SSO login will populate the row.
		return true
	}
	if time.Since(sess.LastCheckedAt) < ssoCheckpointTTL {
		return true
	}

	// unconfirmedTooLong reports whether the grant has gone un-reconfirmed past
	// the fail-open grace window — at which point we stop allowing on faith.
	unconfirmedTooLong := time.Since(sess.LastCheckedAt) > ssoCheckpointGrace

	refreshToken, err := crypto.Decrypt(sess.RefreshToken)
	if err != nil {
		if unconfirmedTooLong {
			logger.Warn("auth", "checkpoint: decrypt failed and grant unconfirmed past grace window, denying", logger.F{"user_id": userID, "error": err})
			return false
		}
		logger.Warn("auth", "checkpoint: decrypt refresh token failed (within grace window, allowing)", logger.F{"user_id": userID, "error": err})
		return true
	}

	resp, err := sso.Introspect(refreshToken, "refresh_token")
	if err != nil {
		if unconfirmedTooLong {
			logger.Warn("auth", "checkpoint: introspect failed and grant unconfirmed past grace window, denying", logger.F{"user_id": userID, "error": err})
			return false
		}
		logger.Warn("auth", "checkpoint: introspect call failed (within grace window, allowing request)", logger.F{"user_id": userID, "error": err})
		return true
	}

	if !resp.Active {
		logger.Info("auth", "checkpoint: IDP reports inactive, revoking local session", logger.F{"user_id": userID})
		// Revoke ALL of this user's Lattice JWTs (access + refresh) by stamping
		// tokens_revoked_at. This is what actually locks the user out: without it,
		// deleting the sso_sessions row would drop us into the sess==nil branch on
		// the next request, which allows — so a revoked SSO user would stay
		// authenticated for the full JWT window. validateLatticeToken rejects any
		// token issued before tokens_revoked_at, so revocation is immediate.
		if revErr := query.RevokeUserTokens(db.DB, int(userID)); revErr != nil {
			logger.Error("auth", "checkpoint: failed to revoke user tokens", logger.F{"user_id": userID, "error": revErr})
			// If we couldn't revoke, still deny this request; the next request
			// will re-introspect and try again.
		}
		if delErr := query.DeleteSSOSession(db.DB, userID); delErr != nil {
			logger.Warn("auth", "checkpoint: failed to delete sso_session", logger.F{"user_id": userID, "error": delErr})
		}
		return false
	}

	if err := query.TouchSSOSession(db.DB, userID); err != nil {
		logger.Warn("auth", "checkpoint: touch failed", logger.F{"user_id": userID, "error": err})
	}
	return true
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
