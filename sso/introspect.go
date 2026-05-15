package sso

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IntrospectResponse is the subset of RFC 7662 §2.2 fields we consume.
type IntrospectResponse struct {
	Active   bool   `json:"active"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Username string `json:"username,omitempty"`
	Sub      string `json:"sub,omitempty"`
	Exp      int64  `json:"exp,omitempty"`
}

// Introspect calls the IDP's RFC 7662 introspection endpoint with the
// caller's client credentials and the user's token. Returns active=false if
// the token is expired, revoked, or the underlying grant is gone.
//
// hint is optional ("access_token" or "refresh_token"); pass "" to let the
// server figure it out.
func Introspect(token, hint string) (*IntrospectResponse, error) {
	cfg := LoadConfig()
	if cfg.IntrospectURL == "" {
		return nil, fmt.Errorf("sso: introspect URL not configured")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("sso: client credentials not configured")
	}

	form := url.Values{}
	form.Set("token", token)
	if hint != "" {
		form.Set("token_type_hint", hint)
	}

	req, err := http.NewRequest("POST", cfg.IntrospectURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("sso: introspect: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sso: introspect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sso: introspect: status %d: %s", resp.StatusCode, string(body))
	}

	var out IntrospectResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("sso: introspect: decode: %w", err)
	}
	return &out, nil
}
