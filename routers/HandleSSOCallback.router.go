package routers

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/sso"
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

	// Check for error from provider (before state validation — some providers
	// return errors without a valid state parameter)
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		logger.Error("sso", "provider returned error", logger.F{"error": errParam, "description": desc})
		http.Redirect(w, r, loginErrorURL("sso_denied"), http.StatusFound)
		return
	}

	// Validate state
	state := r.URL.Query().Get("state")
	if !sso.ValidateState(state) {
		logger.Error("sso", "invalid or expired state parameter")
		http.Redirect(w, r, loginErrorURL("sso_state_expired"), http.StatusFound)
		return
	}

	// Exchange code for tokens
	code := r.URL.Query().Get("code")
	if code == "" {
		logger.Error("sso", "callback missing authorization code")
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	tokenResp, err := sso.ExchangeCode(code)
	if err != nil {
		logger.Error("sso", "token exchange failed", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	// Fetch user info
	userInfo, err := sso.FetchUserInfo(tokenResp.AccessToken)
	if err != nil {
		logger.Error("sso", "userinfo fetch failed", logger.F{"error": err})
		http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
		return
	}

	// Extract user details
	email := sso.GetUserEmail(userInfo)
	if email == "" {
		logger.Error("sso", "no email in userinfo response", logger.F{"userinfo_keys": userInfoKeys(userInfo)})
		http.Redirect(w, r, loginErrorURL("sso_no_email"), http.StatusFound)
		return
	}
	name := sso.GetUserName(userInfo)

	// Find or create user
	user, err := query.GetUserByEmail(db.DB, email)
	if err != nil || user == nil {
		if !cfg.AutoProvision {
			logger.Info("sso", "user not found and auto-provisioning disabled", logger.F{"email": email})
			http.Redirect(w, r, loginErrorURL("sso_no_account"), http.StatusFound)
			return
		}
		// Auto-create with viewer role
		user, err = query.CreateUser(db.DB, query.CreateUserRequest{
			Email:    email,
			Name:     &name,
			AuthType: "sso",
			Role:     "viewer",
		})
		if err != nil {
			logger.Error("sso", "failed to create user", logger.F{"email": email, "error": err})
			http.Redirect(w, r, loginErrorURL("sso_failed"), http.StatusFound)
			return
		}
		logger.Info("sso", "auto-provisioned user", logger.F{"email": email, "user_id": user.ID})
	}

	if !user.Active {
		http.Redirect(w, r, loginErrorURL("account_disabled"), http.StatusFound)
		return
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
		Name: "logged_in", Value: "1", Path: "/",
		Domain: domain, Expires: time.Now().Add(7 * 24 * time.Hour),
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
	})

	logAudit(r, "sso_login", "user", intPtr(user.ID), strPtr(email))

	// Redirect to frontend dashboard
	http.Redirect(w, r, cfg.PostLoginRedirectURL(), http.StatusFound)
}
