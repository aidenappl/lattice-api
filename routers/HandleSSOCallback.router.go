package routers

import (
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/jwt"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/sso"
)

// ssoCfg returns the current SSO configuration from DB/env.
func ssoCfg() *sso.SSOConfig { return sso.LoadConfig() }

func HandleSSOCallback(w http.ResponseWriter, r *http.Request) {
	// Validate state
	state := r.URL.Query().Get("state")
	if !sso.ValidateState(state) {
		http.Error(w, "invalid or expired state parameter", http.StatusBadRequest)
		return
	}

	// Check for error from provider
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		logger.Error("sso", "provider returned error", logger.F{"error": errParam, "description": desc})
		http.Redirect(w, r, "/login?error=sso_denied", http.StatusFound)
		return
	}

	// Exchange code for tokens
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	tokenResp, err := sso.ExchangeCode(code)
	if err != nil {
		logger.Error("sso", "token exchange failed", logger.F{"error": err})
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusFound)
		return
	}

	// Fetch user info
	userInfo, err := sso.FetchUserInfo(tokenResp.AccessToken)
	if err != nil {
		logger.Error("sso", "userinfo fetch failed", logger.F{"error": err})
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusFound)
		return
	}

	// Extract user details
	email := sso.GetUserEmail(userInfo)
	if email == "" {
		logger.Error("sso", "no email in userinfo response")
		http.Redirect(w, r, "/login?error=sso_no_email", http.StatusFound)
		return
	}
	name := sso.GetUserName(userInfo)

	// Find or create user
	user, err := query.GetUserByEmail(db.DB, email)
	if err != nil || user == nil {
		if !ssoCfg().AutoProvision {
			logger.Info("sso", "user not found and auto-provisioning disabled", logger.F{"email": email})
			http.Redirect(w, r, "/login?error=sso_no_account", http.StatusFound)
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
			http.Redirect(w, r, "/login?error=sso_failed", http.StatusFound)
			return
		}
		logger.Info("sso", "auto-provisioned user", logger.F{"email": email, "user_id": user.ID})
	}

	if !user.Active {
		http.Redirect(w, r, "/login?error=account_disabled", http.StatusFound)
		return
	}

	// Issue Lattice JWT tokens
	accessToken, accessExpiry, err := jwt.NewAccessToken(user.ID)
	if err != nil {
		logger.Error("sso", "failed to create access token", logger.F{"error": err})
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusFound)
		return
	}
	refreshToken, refreshExpiry, err := jwt.NewRefreshToken(user.ID)
	if err != nil {
		logger.Error("sso", "failed to create refresh token", logger.F{"error": err})
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusFound)
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

	// Redirect to dashboard
	http.Redirect(w, r, "/", http.StatusFound)
}
