package sso

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/query"
)

var (
	stateStore   = make(map[string]time.Time)
	stateStoreMu sync.Mutex
)

// SSOConfig holds all SSO configuration values.
type SSOConfig struct {
	Enabled        bool
	ClientID       string
	ClientSecret   string
	AuthorizeURL   string
	TokenURL       string
	UserInfoURL    string
	RedirectURL    string
	LogoutURL      string
	Scopes         string
	UserIdentifier string
	ButtonLabel    string
	AutoProvision  bool
	PostLoginURL   string // frontend URL to redirect to after SSO login
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// LoadConfig reads SSO configuration from the database.
// Falls back to environment variables if DB settings don't exist (migration path).
func LoadConfig() *SSOConfig {
	settings, err := query.GetSettingsByPrefix(db.DB, "sso.")
	if err != nil || len(settings) == 0 {
		// Fallback to env vars
		return &SSOConfig{
			Enabled:        env.SSOClientID != "" && env.SSOAuthorizeURL != "",
			ClientID:       env.SSOClientID,
			ClientSecret:   env.SSOClientSecret,
			AuthorizeURL:   env.SSOAuthorizeURL,
			TokenURL:       env.SSOTokenURL,
			UserInfoURL:    env.SSOUserInfoURL,
			RedirectURL:    env.SSORedirectURL,
			LogoutURL:      env.SSOLogoutURL,
			Scopes:         env.SSOScopes,
			UserIdentifier: env.SSOUserIdentifier,
			ButtonLabel:    env.SSOButtonLabel,
			AutoProvision:  env.SSOAutoProvision,
			PostLoginURL:   env.SSOPostLoginURL,
		}
	}

	cfg := &SSOConfig{
		Enabled:        settings["sso.enabled"] == "true",
		ClientID:       settings["sso.client_id"],
		AuthorizeURL:   settings["sso.authorize_url"],
		TokenURL:       settings["sso.token_url"],
		UserInfoURL:    settings["sso.userinfo_url"],
		RedirectURL:    settings["sso.redirect_url"],
		LogoutURL:      settings["sso.logout_url"],
		Scopes:         or(settings["sso.scopes"], "openid email profile"),
		UserIdentifier: or(settings["sso.user_identifier"], "email"),
		ButtonLabel:    or(settings["sso.button_label"], "Sign in with SSO"),
		AutoProvision:  settings["sso.auto_provision"] != "false",
		PostLoginURL:   or(settings["sso.post_login_url"], env.SSOPostLoginURL),
	}

	// Decrypt client secret from DB
	if secret, ok := settings["sso.client_secret"]; ok && secret != "" {
		decrypted, err := crypto.Decrypt(secret)
		if err == nil {
			cfg.ClientSecret = decrypted
		} else {
			cfg.ClientSecret = secret
		}
	}

	return cfg
}

// PostLoginRedirectURL returns the URL to redirect users to after SSO login.
// If PostLoginURL is configured, it uses that. Otherwise it derives a default
// from the RedirectURL by stripping the callback path (e.g.,
// "https://api.example.com/auth/sso/callback" -> "https://api.example.com/").
func (c *SSOConfig) PostLoginRedirectURL() string {
	if c.PostLoginURL != "" && c.PostLoginURL != "/" {
		return c.PostLoginURL
	}
	// Derive from RedirectURL (the SSO callback URL on this API)
	if c.RedirectURL != "" {
		if u, err := url.Parse(c.RedirectURL); err == nil {
			u.Path = "/"
			u.RawQuery = ""
			return u.String()
		}
	}
	return "/"
}

func IsConfigured() bool {
	cfg := LoadConfig()
	return cfg.Enabled && cfg.ClientID != "" && cfg.AuthorizeURL != "" && cfg.TokenURL != ""
}

// Config returns the public SSO configuration for the frontend
func Config() map[string]any {
	cfg := LoadConfig()
	if !cfg.Enabled || cfg.ClientID == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":      true,
		"button_label": cfg.ButtonLabel,
		"login_url":    "/auth/sso/login",
	}
}

// generateState creates a random state parameter and stores it for validation
func generateState() string {
	b := make([]byte, 32)
	rand.Read(b)
	state := base64.URLEncoding.EncodeToString(b)

	stateStoreMu.Lock()
	stateStore[state] = time.Now().Add(10 * time.Minute)
	// Cleanup old states
	for k, v := range stateStore {
		if time.Now().After(v) {
			delete(stateStore, k)
		}
	}
	stateStoreMu.Unlock()

	return state
}

// ValidateState checks that a state parameter is valid and not expired, then removes it.
func ValidateState(state string) bool {
	stateStoreMu.Lock()
	defer stateStoreMu.Unlock()
	expiry, exists := stateStore[state]
	if !exists || time.Now().After(expiry) {
		return false
	}
	delete(stateStore, state)
	return true
}

// LoginHandler redirects the user to the SSO provider's authorization URL
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	cfg := LoadConfig()
	if !cfg.Enabled || cfg.ClientID == "" || cfg.AuthorizeURL == "" {
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}

	state := generateState()

	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {cfg.Scopes},
		"state":         {state},
	}

	http.Redirect(w, r, cfg.AuthorizeURL+"?"+params.Encode(), http.StatusFound)
}

// TokenResponse from the OAuth2 token endpoint
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

// ExchangeCode exchanges an authorization code for tokens
func ExchangeCode(code string) (*TokenResponse, error) {
	cfg := LoadConfig()

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
	}

	resp, err := http.PostForm(cfg.TokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	return &tokenResp, nil
}

// FetchUserInfo calls the userinfo endpoint with the access token.
//
// The function handles two response formats transparently:
//  1. Flat OIDC-style JSON (e.g. from /oauth/userinfo):
//     {"sub":"123","email":"a@b.com", ...}
//  2. Forta envelope JSON (e.g. from /auth/self):
//     {"success":true,"data":{"id":123,"email":"a@b.com", ...}}
//
// When the response is a Forta envelope (has "success" and "data" keys where
// "data" is a JSON object), the inner object is returned so that callers like
// GetUserEmail can find fields at the top level.
func FetchUserInfo(accessToken string) (map[string]any, error) {
	cfg := LoadConfig()
	if cfg.UserInfoURL == "" {
		return nil, fmt.Errorf("SSO userinfo URL not configured")
	}

	req, err := http.NewRequest("GET", cfg.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var userInfo map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo: %w", err)
	}

	// Unwrap Forta envelope: if the response has a "success" key and "data" is
	// a nested object, return the inner data map so callers find user fields
	// (email, name, etc.) at the top level.
	if _, hasSuccess := userInfo["success"]; hasSuccess {
		if data, ok := userInfo["data"].(map[string]any); ok {
			return data, nil
		}
	}

	return userInfo, nil
}

// GetUserIdentifier extracts the configured identifier field from userinfo
func GetUserIdentifier(userInfo map[string]any) string {
	cfg := LoadConfig()
	field := cfg.UserIdentifier
	if field == "" {
		field = "email"
	}

	// Try the direct field
	if val, ok := userInfo[field]; ok {
		return fmt.Sprint(val)
	}

	// Common fallbacks
	if email, ok := userInfo["email"]; ok {
		return fmt.Sprint(email)
	}

	return ""
}

// GetUserName extracts a display name from userinfo
func GetUserName(userInfo map[string]any) string {
	if name, ok := userInfo["name"]; ok {
		s := fmt.Sprint(name)
		if s != "" && s != "<nil>" {
			return s
		}
	}
	if given, ok := userInfo["given_name"]; ok {
		s := fmt.Sprint(given)
		if s != "" && s != "<nil>" {
			name := s
			if family, ok := userInfo["family_name"]; ok {
				fs := fmt.Sprint(family)
				if fs != "" && fs != "<nil>" {
					name += " " + fs
				}
			}
			return strings.TrimSpace(name)
		}
	}
	// Fallback: use the part before @ in the email
	if email := GetUserEmail(userInfo); email != "" {
		if at := strings.Index(email, "@"); at > 0 {
			return email[:at]
		}
	}
	return ""
}

// GetUserEmail extracts email from userinfo, trying common field names.
// Some providers use "preferred_username" or "upn" instead of "email".
func GetUserEmail(userInfo map[string]any) string {
	for _, field := range []string{"email", "preferred_username", "upn", "mail"} {
		if val, ok := userInfo[field]; ok {
			s := fmt.Sprint(val)
			if s != "" && s != "<nil>" && strings.Contains(s, "@") {
				return s
			}
		}
	}
	return ""
}
