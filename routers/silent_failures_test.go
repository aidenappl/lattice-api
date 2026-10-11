package routers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
)

func logoutRequest(user *structs.User) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, user))
}

func TestLogoutAlwaysClearsCookiesAndReportsRevocationFailure(t *testing.T) {
	origRevoke := revokeUserTokens
	t.Cleanup(func() { revokeUserTokens = origRevoke })

	dbDown := errors.New("dial tcp 10.0.0.5:3306: connect: connection refused")
	tests := []struct {
		name      string
		authType  string
		revokeErr error
		status    int
	}{
		{"local user, revoke ok", "local", nil, http.StatusOK},
		{"local user, revoke fails", "local", dbDown, http.StatusInternalServerError},
		{"sso user without a session cookie, revoke ok", "sso", nil, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revokeUserTokens = func(db.Queryable, int) error { return tt.revokeErr }

			rw := httptest.NewRecorder()
			HandleLogout(rw, logoutRequest(&structs.User{ID: 7, AuthType: tt.authType}))

			if rw.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rw.Code, tt.status, rw.Body.String())
			}
			cleared := strings.Contains(strings.Join(rw.Header().Values("Set-Cookie"), ";"), "lattice-access-token=;")
			if !cleared {
				t.Error("logout must always clear the auth cookies, even when revocation fails")
			}
			if tt.status != http.StatusOK {
				var resp responder.ErrorResponse
				_ = json.NewDecoder(rw.Body).Decode(&resp)
				if resp.Success || strings.Contains(rw.Body.String(), "connection refused") {
					t.Errorf("body = %s; want a failure that does not leak the internal error", rw.Body.String())
				}
			}
		})
	}
}

func TestSettingsSavesReportFailure(t *testing.T) {
	orig := saveSettings
	t.Cleanup(func() { saveSettings = orig })

	handlers := []struct {
		name    string
		handler http.HandlerFunc
		body    string
	}{
		{"sso", HandleUpdateSSOConfig, `{"enabled":true,"client_id":" abc "}`},
		{"smtp", HandleUpdateSMTPConfig, `{"enabled":true,"host":" smtp.example.com "}`},
	}
	for _, h := range handlers {
		t.Run(h.name+" save fails", func(t *testing.T) {
			saveSettings = func(settingsForm) error { return errors.New("commit: driver: bad connection") }
			rw := httptest.NewRecorder()
			h.handler(rw, httptest.NewRequest(http.MethodPut, "/admin/"+h.name+"-config", strings.NewReader(h.body)))
			if rw.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: a save that did not happen was reported as done", rw.Code)
			}
		})
	}
}

func TestSettingsFormStagesOnlyProvidedFields(t *testing.T) {
	host, on := "  smtp.example.com ", false
	var f settingsForm
	f.str("smtp.host", &host)
	f.str("smtp.username", nil)
	f.boolean("smtp.enabled", &on)
	f.boolean("smtp.tls", nil)
	f.raw("smtp.password", "enc:v1:abc")

	want := settingsForm{{"smtp.host", "smtp.example.com"}, {"smtp.enabled", "false"}, {"smtp.password", "enc:v1:abc"}}
	if len(f) != len(want) {
		t.Fatalf("form = %v, want %v", f, want)
	}
	for i := range want {
		if f[i] != want[i] {
			t.Errorf("form[%d] = %v, want %v", i, f[i], want[i])
		}
	}
}
