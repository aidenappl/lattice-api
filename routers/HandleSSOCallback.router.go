package routers

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	ssolib "github.com/aidenappl/go-forta/sso"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/structs"
)

// userInfoKeys returns a comma-separated list of keys from a userinfo map,
// useful for debugging when expected fields (like email) are missing.
func userInfoKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// ssoCfg returns the current SSO configuration from DB/env.
func ssoCfg() *sso.SSOConfig { return sso.LoadConfig() }

func HandleSSOCallback(w http.ResponseWriter, r *http.Request) {
	cfg := ssoCfg()

	// Helper: redirect to the frontend login page with an error code.
	// Derives the frontend URL from the SSO config.
	loginErrorURL := func(errorCode string) string {
		base := cfg.PostLoginRedirectURL()
		// Ensure we redirect to /login on the frontend, not /
		if u, err := url.Parse(base); err == nil {
			u.Path = "/login"
			u.RawQuery = "error=" + url.QueryEscape(errorCode)
			return u.String()
		}
		return "/login?error=" + url.QueryEscape(errorCode)
	}

	// hasSession reports whether the browser already holds a Lattice session
	// cookie — used to tolerate a benign double-callback (provider redirect
	// chains) now that state is single-use, without weakening CSRF protection.
	hasSession := func() bool {
		c, err := r.Cookie("lattice-access-token")
		return err == nil && c.Value != ""
	}

	// The state cookie is single-use — always clear it once we reach the callback.
	sso.ClearStateCookie(w)

	// Check for error from provider (before state validation — some providers
	// return errors without a valid state parameter)
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		logger.Error("sso", "provider returned error", logger.F{"error": errParam, "description": desc})
		http.Redirect(w, r, loginErrorURL("sso_denied"), http.StatusFound)
		return
	}

	// Validate state. Two independent checks must pass:
	//   1. The state must be bound to THIS browser — the value in the
	//      lattice-sso-state HttpOnly cookie (set at /auth/sso/login) must match
	//      the state returned by the IDP. This is the CSRF defense: an attacker
	//      who forges a callback cannot also set this browser's cookie.
	//   2. The state must be present and unexpired in the DB, and is consumed
	//      ATOMICALLY and single-use by sso.StateStore.ConsumeState.
	state := r.URL.Query().Get("state")
	stateCookie, cookieErr := r.Cookie(sso.SSOStateCookie)
	if cookieErr != nil || stateCookie.Value == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(state)) != 1 {
		// If the browser already completed login (double-callback), send it on.
		if hasSession() {
			http.Redirect(w, r, cfg.PostLoginRedirectURL(), http.StatusFound)
			return
		}
		logger.Error("sso", "state cookie missing or does not match callback state")
		http.Redirect(w, r, loginErrorURL("sso_state_expired"), http.StatusFound)
		return
	}
	// The server-side record carries the PKCE verifier and the nonce, and consuming
	// it is ATOMIC — see sso.StateStore.ConsumeState. The old ValidateState did a
	// read followed by an unconditional delete and returned a bool, so two
	// concurrent callbacks presenting the same state both passed.
	stateData, err := ssolib.ConsumeState(r.Context(), sso.NewStateStore(), state)
	if err != nil {
		if hasSession() {
			http.Redirect(w, r, cfg.PostLoginRedirectURL(), http.StatusFound)
			return
		}
		logger.Error("sso", "invalid, expired or already-consumed state")
		http.Redirect(w, r, loginErrorURL("sso_state_expired"), http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		logger.Error("sso", "callback missing authorization code")
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	provider := cfg.Provider()
	adapter, err := ssolib.NewAdapter(r.Context(), provider)
	if err != nil {
		logger.Error("sso", "adapter build failed", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	// ── One exchange, with the PKCE verifier attached ────────────────────────
	//
	// This replaces ExchangeCode's JSON → Basic → body fallback chain. That chain
	// could never have worked as intended: the first attempt to reach a conforming
	// server consumes the single-use code, so the fallbacks could only ever receive
	// invalid_grant. It also dropped the verifier entirely, so PKCE was configured
	// and defended nothing.
	identity, tokens, err := adapter.Exchange(r.Context(), code, stateData.Verifier, stateData.Nonce)
	if err != nil {
		// A double-callback lands here now that the code is genuinely single-use.
		// If the browser already holds a session from the first leg, send it on.
		if hasSession() {
			logger.Info("sso", "exchange failed but browser already has a session, continuing")
			http.Redirect(w, r, cfg.PostLoginRedirectURL(), http.StatusFound)
			return
		}
		logger.Error("sso", "token exchange failed", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	email := identity.Email
	if email == "" {
		logger.Error("sso", "no email in identity")
		http.Redirect(w, r, loginErrorURL("sso_no_email"), http.StatusFound)
		return
	}
	name := ""
	if identity.Name != nil {
		name = *identity.Name
	}
	picture := ""
	if identity.Picture != nil {
		picture = *identity.Picture
	}

	// ── The subject is now the REAL `sub` ────────────────────────────────────
	//
	// It comes from the library, which reads the standard claim. It is no longer
	// sso.GetUserIdentifier(), which read `sso.user_identifier` — "email" in
	// production — and fell back to "email" regardless, writing an address into a
	// column documented as the OIDC `sub`. Identity keyed on a reassignable address
	// is an account-takeover primitive.
	subject := identity.Subject

	// Find user: try sso_subject first (stable), then email+auth_type=sso
	// This allows the same email to have separate local and SSO accounts.
	var user *structs.User
	if subject != "" {
		user, _ = query.GetUserBySSOSubject(db.DB, subject)
	}
	if user == nil {
		// Look for an existing SSO account with this email (not local accounts)
		user, _ = query.GetUserByEmailAndAuthType(db.DB, email, "sso")
	}

	if user == nil {
		if !cfg.AutoProvision {
			logger.Info("sso", "user not found and auto-provisioning disabled", logger.F{"email": email})
			http.Redirect(w, r, loginErrorURL("sso_no_account"), http.StatusFound)
			return
		}
		// Auto-create with pending role (requires admin approval)
		user, err = query.CreateUser(db.DB, query.CreateUserRequest{
			Email:           email,
			Name:            &name,
			AuthType:        "sso",
			SSOSubject:      &subject,
			ProfileImageURL: &picture,
			Role:            "pending",
		})
		if err != nil {
			logger.Error("sso", "failed to create user", logger.F{"email": email, "error": err})
			http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
			return
		}
		logger.Info("sso", "auto-provisioned user", logger.F{"email": email, "user_id": user.ID, "sso_subject": subject})
	} else if subject != "" && (user.SSOSubject == nil || sso.LooksLikeEmail(*user.SSOSubject)) {
		// ── Heal the stored subject ──────────────────────────────────────────
		//
		// Two cases, both writing the real `sub` over what is there:
		//
		//  1. nil — a user who logged in before sso_subject existed at all.
		//  2. AN EMAIL ADDRESS — a user whose subject was written by the old
		//     GetUserIdentifier, which returned `sso.user_identifier` ("email" in
		//     production). This is the one-time repair of that bug. A real `sub`
		//     from forta-api is a UUID and can never contain "@", so the test
		//     cannot misfire on a legitimate value.
		//
		// Healing on login rather than by a migration script is deliberate: the
		// correct `sub` is only knowable from a live token, so a script would have
		// had to match on email and trust that mapping. There was exactly one such
		// row when this shipped, and its owner logging in fixes it correctly.
		//
		// ⚠️ The user was matched by EMAIL to get here (GetUserByEmailAndAuthType),
		// which is the very thing this change is moving away from. That is safe only
		// because the match is scoped to auth_type='sso' and happens once — after
		// this write, the subject lookup succeeds and the email path is never taken
		// for this user again. When every row has a real subject, the email fallback
		// should be deleted.
		previous := "nil"
		if user.SSOSubject != nil {
			previous = *user.SSOSubject
		}
		if err := query.UpdateUserSSOSubject(db.DB, user.ID, subject); err != nil {
			// Non-fatal: the login proceeds. The heal is retried on the next login.
			logger.Error("sso", "failed to heal sso_subject", logger.F{"user_id": user.ID, "error": err})
		} else {
			logger.Info("sso", "healed sso_subject to the real OIDC sub",
				logger.F{"user_id": user.ID, "previous": previous, "sso_subject": subject})
		}
	}

	// Update profile image on each login (it might change at the provider)
	if picture != "" {
		_, _ = query.UpdateUser(db.DB, user.ID, query.UpdateUserRequest{ProfileImageURL: &picture})
	}

	if !user.Active {
		http.Redirect(w, r, loginErrorURL("account_disabled"), http.StatusFound)
		return
	}

	// Persist the IdP tokens so the checkpoint can introspect the upstream grant.
	// Encryption at rest is the SessionStore's job — see sso/sessionstore.go.
	//
	// ⚠️ Until this change the table these rows go into did not exist, so this write
	// failed silently on every login and the checkpoint had nothing to read.
	// db/db.go now creates it.
	if err := sso.NewSessionStore().SaveSession(r.Context(), int64(user.ID), ssolib.Session{
		Provider: sso.ProviderSlug,
		Subject:  subject,
		Tokens:   *tokens,
	}); err != nil {
		// Non-fatal: the login has succeeded and the user gets their session. The cost
		// is that this session is not checkpointed until the next login.
		logger.Error("sso", "failed to persist sso session", logger.F{"error": err, "user_id": user.ID})
	}

	// Issue Lattice JWT tokens
	accessToken, accessExpiry, err := jwt.NewAccessToken(user.ID)
	if err != nil {
		logger.Error("sso", "failed to create access token", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}
	refreshToken, refreshExpiry, err := jwt.NewRefreshToken(user.ID)
	if err != nil {
		logger.Error("sso", "failed to create refresh token", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	// Set cookies (same as local login)
	secure := env.Environment == "production"
	domain := env.CookieDomain

	http.SetCookie(w, &http.Cookie{
		Name: "lattice-access-token", Value: accessToken, Path: "/",
		Domain: domain, Expires: accessExpiry,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "lattice-refresh-token", Value: refreshToken, Path: "/",
		Domain: domain, Expires: refreshExpiry,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "lattice-logged-in", Value: "1", Path: "/",
		Domain: domain, Expires: time.Now().Add(7 * 24 * time.Hour),
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
	})

	logAudit(r, "sso_login", "user", intPtr(user.ID), strPtr(email))

	// Redirect to frontend dashboard
	http.Redirect(w, r, cfg.PostLoginRedirectURL(), http.StatusFound)
}
