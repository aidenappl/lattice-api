package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	ssolib "github.com/aidenappl/go-forta/sso"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// State is stored in the DB via the settings table (so it survives restarts and
// is single-use) AND bound to the browser via an HttpOnly cookie set at
// /auth/sso/login, so the callback can prove the request originated from a login
// this browser started (CSRF defense).
const SSOStateCookie = "lattice-sso-state"

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
	IntrospectURL  string
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
			IntrospectURL:  env.SSOIntrospectURL,
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
		IntrospectURL:  strings.TrimSpace(or(settings["sso.introspect_url"], env.SSOIntrospectURL)),
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

// generateState creates a random state parameter and stores it in the DB for validation.
// Using the DB (instead of in-memory) ensures states survive API restarts.
// Returns an error if the OS CSPRNG fails — the caller MUST NOT proceed with a
// zero/weak state.
// StateStore implements ssolib.StateStore over the settings table.
//
// It replaces the old generateState/ValidateState pair, which stored only an
// expiry timestamp — there was no nonce and no PKCE verifier to store, because
// neither existed. The library's record carries both.
type StateStore struct{}

// NewStateStore returns a StateStore over the package-level DB handle.
func NewStateStore() *StateStore { return &StateStore{} }

const statePrefix = "sso_state:"

// SaveState persists an in-flight login record and best-effort sweeps dead ones.
func (s *StateStore) SaveState(_ context.Context, state string, data []byte, _ time.Time) error {
	if err := query.SetSetting(db.DB, statePrefix+state, string(data)); err != nil {
		return fmt.Errorf("sso: persist state: %w", err)
	}
	go sweepExpiredStates()
	return nil
}

// ConsumeState atomically returns and deletes a state record.
//
// ─────────────────────────────────────────────────────────────────────────────
// ⚠️ THIS FIXES A RACE THE OLD ValidateState HAD.
//
// The previous implementation did GetSetting, then DeleteSetting, and returned a
// bool. DeleteSetting reports success whether or not a row was there, so two
// concurrent callbacks presenting the same state BOTH read it and BOTH proceeded
// — which is the window an attacker replaying a captured callback URL alongside
// the real one is aiming at.
//
// query.DeleteSettingExisted returns RowsAffected, and MariaDB's row lock
// guarantees exactly one of N concurrent callers sees true. Winning the DELETE —
// not having read the row — is what authorises using the record.
// ─────────────────────────────────────────────────────────────────────────────
func (s *StateStore) ConsumeState(_ context.Context, state string) ([]byte, error) {
	key := statePrefix + state

	raw, err := query.GetSetting(db.DB, key)
	if err != nil || raw == "" {
		return nil, ssolib.ErrNoState
	}

	deleted, err := query.DeleteSettingExisted(db.DB, key)
	if err != nil {
		return nil, fmt.Errorf("sso: consume state: %w", err)
	}
	if !deleted {
		return nil, ssolib.ErrNoState
	}
	return []byte(raw), nil
}

// sweepExpiredStates prunes expired or unparseable records.
func sweepExpiredStates() {
	states, err := query.GetSettingsByPrefix(db.DB, statePrefix)
	if err != nil {
		return
	}
	for k, v := range states {
		var sd ssolib.StateData
		if err := json.Unmarshal([]byte(v), &sd); err != nil {
			// Includes every record written by the OLD format, which stored a bare
			// RFC3339 timestamp rather than JSON. Those can never be consumed by the
			// library, so deleting them is strictly better than leaving them.
			_ = query.DeleteSetting(db.DB, k)
			continue
		}
		if time.Now().After(sd.ExpiresAt) {
			_ = query.DeleteSetting(db.DB, k)
		}
	}
}

// LoginHandler redirects the user to the provider's authorization URL.
//
// It now goes through the library, which means the URL carries a PKCE S256
// challenge and (for kind=oidc) a nonce — neither of which this service sent
// before. The verifier and nonce are held SERVER-SIDE in the state record, so the
// callback validates against values the browser never possessed.
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	cfg := LoadConfig()
	if !cfg.Enabled || cfg.ClientID == "" || cfg.AuthorizeURL == "" {
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}

	provider := cfg.Provider()
	adapter, err := ssolib.NewAdapter(r.Context(), provider)
	if err != nil {
		logger.Error("sso", "adapter build failed", logger.F{"error": err})
		http.Error(w, "SSO misconfigured", http.StatusInternalServerError)
		return
	}

	state, nonce, verifier, err := ssolib.GenerateState(r.Context(), NewStateStore(), provider.Slug, "")
	if err != nil {
		logger.Error("sso", "state generation failed", logger.F{"error": err})
		http.Error(w, "failed to initialize login", http.StatusInternalServerError)
		return
	}

	authURL, err := adapter.AuthCodeURL(state, nonce, verifier)
	if err != nil {
		logger.Error("sso", "authorize url build failed", logger.F{"error": err})
		http.Error(w, "failed to initialize login", http.StatusInternalServerError)
		return
	}

	// The browser-bound state cookie is KEPT as defence in depth, even though the
	// state record is now server-side and single-use. It costs nothing and it means
	// a callback arriving in a different browser than the one that started the login
	// is rejected before any database work happens. SameSite=Lax so it survives the
	// top-level GET redirect back from the IdP; Path scoped to /auth/sso so it is
	// presented on nothing else.
	http.SetCookie(w, &http.Cookie{
		Name:     SSOStateCookie,
		Value:    state,
		Path:     "/auth/sso",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   env.Environment == "production",
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, authURL, http.StatusFound)
}

// ClearStateCookie writes an expired state cookie to the response, consuming the
// browser-side binding after a callback (single-use).
func ClearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SSOStateCookie,
		Value:    "",
		Path:     "/auth/sso",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   env.Environment == "production",
		SameSite: http.SameSiteLaxMode,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// WHAT USED TO BE HERE, AND WHY IT IS GONE
//
// This file previously carried the whole OAuth2 client: ExchangeCode,
// exchangeWithJSON / exchangeWithBasicAuth / exchangeWithBodyAuth, doTokenRequest,
// FetchUserInfo, GetUserIdentifier, GetUserName, GetUserPicture, GetUserEmail, and
// a local Introspect. All of it is now in github.com/aidenappl/go-forta/sso.
//
// Three of those were actively harmful and are worth naming so nobody restores
// them:
//
//  1. THE THREE-SHAPE TOKEN EXCHANGE. ExchangeCode tried a JSON body, then HTTP
//     Basic, then body credentials, in sequence, until one worked. Against an
//     authorization server that treats codes as single-use — which any conforming
//     one does, and forta-api now provably does — the FIRST attempt that reaches
//     the server consumes the code, so the fallbacks can only ever receive
//     invalid_grant. It was not a compatibility layer; it was a guaranteed wasted
//     round trip on every login, visible in forta-api's logs as a 400 immediately
//     followed by a 200.
//
//  2. NO PKCE AT ALL. There was no code_challenge and no verifier, so a leaked
//     authorization code was redeemable by whoever held it.
//
//  3. GetUserIdentifier RETURNED AN EMAIL AND IT WAS STORED AS THE SUBJECT. It
//     read whatever `sso.user_identifier` named — "email" in production — and fell
//     back to "email" regardless, and the caller wrote that into
//     users.sso_subject under a comment claiming it was the OIDC `sub`. Identity
//     keyed on a reassignable address is an account-takeover primitive. The
//     library returns the real `sub`; see HandleSSOCallback for the one-time heal
//     of the row this left behind.
//
// Do not re-add a local copy of any of it.
// ─────────────────────────────────────────────────────────────────────────────
