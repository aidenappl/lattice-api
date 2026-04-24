package routers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/tools"
)

// HandleGetSSOConfig returns the full SSO configuration (with client_secret masked).
// GET /admin/sso-config
func HandleGetSSOConfig(w http.ResponseWriter, r *http.Request) {
	cfg := sso.LoadConfig()

	// Mask the secret for display
	maskedSecret := ""
	if cfg.ClientSecret != "" {
		if len(cfg.ClientSecret) > 4 {
			maskedSecret = "••••••••" + cfg.ClientSecret[len(cfg.ClientSecret)-4:]
		} else {
			maskedSecret = "••••••••"
		}
	}

	responder.New(w, map[string]any{
		"enabled":         cfg.Enabled,
		"client_id":       cfg.ClientID,
		"client_secret":   maskedSecret,
		"authorize_url":   cfg.AuthorizeURL,
		"token_url":       cfg.TokenURL,
		"userinfo_url":    cfg.UserInfoURL,
		"redirect_url":    cfg.RedirectURL,
		"logout_url":      cfg.LogoutURL,
		"scopes":          cfg.Scopes,
		"user_identifier": cfg.UserIdentifier,
		"button_label":    cfg.ButtonLabel,
		"auto_provision":  cfg.AutoProvision,
		"post_login_url":  cfg.PostLoginURL,
	}, "SSO configuration")
}

// HandleUpdateSSOConfig updates SSO configuration in the database.
// PUT /admin/sso-config
func HandleUpdateSSOConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled        *bool   `json:"enabled"`
		ClientID       *string `json:"client_id"`
		ClientSecret   *string `json:"client_secret"`
		AuthorizeURL   *string `json:"authorize_url"`
		TokenURL       *string `json:"token_url"`
		UserInfoURL    *string `json:"userinfo_url"`
		RedirectURL    *string `json:"redirect_url"`
		LogoutURL      *string `json:"logout_url"`
		Scopes         *string `json:"scopes"`
		UserIdentifier *string `json:"user_identifier"`
		ButtonLabel    *string `json:"button_label"`
		AutoProvision  *bool   `json:"auto_provision"`
		PostLoginURL   *string `json:"post_login_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Helper to set a string setting if provided (trims whitespace)
	setIf := func(key string, val *string) {
		if val != nil {
			_ = query.SetSetting(db.DB, key, strings.TrimSpace(*val))
		}
	}
	// Helper to set a bool setting if provided
	setBoolIf := func(key string, val *bool) {
		if val != nil {
			v := "false"
			if *val {
				v = "true"
			}
			_ = query.SetSetting(db.DB, key, v)
		}
	}

	// Validate SSO endpoint URLs to prevent SSRF
	for _, u := range []*string{body.TokenURL, body.UserInfoURL, body.AuthorizeURL, body.LogoutURL} {
		if u != nil && *u != "" {
			if err := tools.ValidateExternalURL(*u); err != nil {
				responder.SendError(w, http.StatusBadRequest, "invalid SSO URL: "+err.Error())
				return
			}
		}
	}

	setBoolIf("sso.enabled", body.Enabled)
	setIf("sso.client_id", body.ClientID)

	// Only update secret if non-empty and not the masked value
	if body.ClientSecret != nil && *body.ClientSecret != "" && !strings.HasPrefix(*body.ClientSecret, "••") {
		encrypted, err := crypto.Encrypt(*body.ClientSecret)
		if err == nil {
			_ = query.SetSetting(db.DB, "sso.client_secret", encrypted)
		}
	}

	setIf("sso.authorize_url", body.AuthorizeURL)
	setIf("sso.token_url", body.TokenURL)
	setIf("sso.userinfo_url", body.UserInfoURL)
	setIf("sso.redirect_url", body.RedirectURL)
	setIf("sso.logout_url", body.LogoutURL)
	setIf("sso.scopes", body.Scopes)
	setIf("sso.user_identifier", body.UserIdentifier)
	setIf("sso.button_label", body.ButtonLabel)
	setBoolIf("sso.auto_provision", body.AutoProvision)
	setIf("sso.post_login_url", body.PostLoginURL)

	logAudit(r, "update", "sso_config", nil, nil)

	responder.New(w, nil, "SSO configuration updated")
}
