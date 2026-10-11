package sso

import (
	"net/url"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/query"
)

// SSOConfig holds all SSO configuration values.
type SSOConfig struct {
	Enabled  bool
	ClientID string

	// IssuerURL, when set, upgrades this provider from OAuth2 to OIDC.
	//
	// ─────────────────────────────────────────────────────────────────────────
	// ⚠️ A SECURITY UPGRADE, NOT A CONFIGURATION PREFERENCE.
	//
	// Without an issuer there is no discovery document and no id_token, so the
	// subject arrives from a UserInfo call authenticated by a bearer token and
	// signed by nothing — anything able to obtain an access token can become that
	// user. With an issuer the library verifies a signed id_token, checks the
	// nonce, and surfaces the `sid` back-channel logout needs to name a session.
	//
	// Empty keeps the legacy OAuth2 path so an existing deployment survives the
	// upgrade. It is a state to leave, not to stay in.
	// ─────────────────────────────────────────────────────────────────────────
	IssuerURL string

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
		ClientID:       strings.TrimSpace(settings["sso.client_id"]),
		IssuerURL:      strings.TrimSpace(settings["sso.issuer_url"]),
		AuthorizeURL:   strings.TrimSpace(settings["sso.authorize_url"]),
		TokenURL:       strings.TrimSpace(settings["sso.token_url"]),
		UserInfoURL:    strings.TrimSpace(settings["sso.userinfo_url"]),
		RedirectURL:    strings.TrimSpace(settings["sso.redirect_url"]),
		LogoutURL:      strings.TrimSpace(settings["sso.logout_url"]),
		Scopes:         strings.TrimSpace(or(settings["sso.scopes"], "openid email profile")),
		UserIdentifier: strings.TrimSpace(or(settings["sso.user_identifier"], "email")),
		ButtonLabel:    or(settings["sso.button_label"], "Sign in with SSO"),
		AutoProvision:  settings["sso.auto_provision"] != "false",
		PostLoginURL:   strings.TrimSpace(or(settings["sso.post_login_url"], env.SSOPostLoginURL)),
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
// ProviderEntry is one login button in the shared SSO config contract.
//
// ⚠️ THE SHAPE IS SHARED with monitor-core and openbucket-api so a login page
// written once renders against any of them. Modelled on Zulip's server_settings.
// Do not add a field here without adding it to the other two.
type ProviderEntry struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	DisplayIcon *string `json:"display_icon"`

	// ⚠️ Null is CONTRACTUAL for display_icon and the colours: it means "render a
	// plain text button in the default style", which is the state a provider is in
	// before branding is configured. Every client must handle it.
	ButtonColor     *string `json:"button_color"`
	ButtonTextColor *string `json:"button_text_color"`

	// LoginURL is COMPUTED, never stored.
	//
	// ⚠️ A stored login URL is an administrator-controlled value that an
	// unauthenticated page turns into a link the user is told to click — an
	// open-redirect primitive on your own domain, and a phishing lure that survives
	// review because the page really is yours.
	LoginURL string `json:"login_url"`

	SortOrder int `json:"sort_order"`
}

// Config returns the PUBLIC provider discovery payload.
//
// ─────────────────────────────────────────────────────────────────────────────
// THIS SERVICE IS STILL SINGLE-PROVIDER, and the response is still an ARRAY.
//
// The array is the shared contract; this service simply happens to put at most
// one entry in it. Emitting the shared shape now means the login page is written
// once, against the contract, and does not change when the sso_providers table
// lands here — at which point this function grows a loop and nothing else moves.
//
// ⚠️ UNAUTHENTICATED. Display data only — never an issuer URL, a client_id or a
// scope list. Adding a field here publishes it.
//
// `enabled` and `button_label` are retained alongside `providers` for one deploy
// so a login page still running the previous bundle keeps working while the new
// one rolls out. They can be dropped once every frontend reads `providers`.
// ─────────────────────────────────────────────────────────────────────────────
func Config() map[string]any {
	cfg := LoadConfig()
	if !cfg.Enabled || cfg.ClientID == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" {
		return map[string]any{
			"enabled":   false,
			"providers": []ProviderEntry{},
		}
	}

	label := cfg.ButtonLabel
	if label == "" {
		label = "Sign in with SSO"
	}

	return map[string]any{
		// Legacy fields, retained for one deploy. See the note above.
		"enabled":      true,
		"button_label": label,
		"login_url":    "/auth/sso/login",

		"providers": []ProviderEntry{{
			Name:        ProviderSlug,
			DisplayName: label,
			// Branding has no storage on this service yet — the sso_providers table
			// is where those columns live, and it has not landed here. Null is the
			// contractual "text button in the default style", so the login page
			// renders correctly today and picks up branding for free later.
			DisplayIcon:     nil,
			ButtonColor:     nil,
			ButtonTextColor: nil,
			LoginURL:        "/auth/sso/login",
			SortOrder:       0,
		}},
	}
}
