package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func dummyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	})
}

func TestCSRFGetPassesThrough(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/stacks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET should pass, got status %d", rr.Code)
	}
}

func TestCSRFPostWithMatchingTokenPasses(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())
	token := "test-csrf-token-value"

	req := httptest.NewRequest(http.MethodPost, "/admin/stacks", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	req.Header.Set(csrfHeaderName, token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("POST with matching CSRF should pass, got status %d", rr.Code)
	}
}

func TestCSRFPostMissingCookie(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodPost, "/admin/stacks", strings.NewReader("{}"))
	req.Header.Set(csrfHeaderName, "some-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if code, _ := resp["error_code"].(float64); int(code) != 4030 {
		t.Errorf("error_code = %v, want 4030", resp["error_code"])
	}
}

func TestCSRFPostMismatchedToken(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodPost, "/admin/stacks", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "cookie-token"})
	req.Header.Set(csrfHeaderName, "different-header-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if code, _ := resp["error_code"].(float64); int(code) != 4031 {
		t.Errorf("error_code = %v, want 4031", resp["error_code"])
	}
}

func TestCSRFBearerAuthSkips(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodPost, "/admin/stacks", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Bearer auth should skip CSRF, got status %d", rr.Code)
	}
}

func TestCSRFExemptPaths(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	exemptPaths := []string{
		"/auth/login",
		"/auth/refresh",
		"/ws/worker",
		"/api/deploy/123",
		"/auth/sso/callback",
	}

	for _, path := range exemptPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("exempt path %s should pass, got status %d", path, rr.Code)
			}
		})
	}
}

func TestCSRFSetsCookieOnFirstVisit(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	cookies := rr.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == csrfCookieName {
			found = true
			if c.Value == "" {
				t.Error("CSRF cookie value is empty")
			}
		}
	}
	if !found {
		t.Error("CSRF cookie not set on first visit")
	}
}

// TestCSRFExemptsBackchannelLogout guards a failure invisible from both ends.
//
// ─────────────────────────────────────────────────────────────────────────────
// The back-channel logout notification is a server-to-server POST from the
// identity provider: no cookie, no Bearer token, no custom header. Without an
// exemption it falls through to the double-submit check it can never satisfy and
// is refused 403. The provider then retries six times, marks the delivery
// exhausted, and revocation silently stays at poll speed — while the endpoint
// looks like a broken receiver rather than a blocked one.
//
// monitor-core shipped exactly that on 2026-08-08. Its routing test passed
// throughout, because the router was never what rejected the request.
// ─────────────────────────────────────────────────────────────────────────────
func TestCSRFExemptsBackchannelLogout(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodPost, "/auth/sso/backchannel-logout",
		strings.NewReader("logout_token=signed.jwt.here"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("CSRF refused the back-channel logout POST (status %d, body %s).\n\n"+
			"The provider sends this with no cookie and no auth headers, so without the "+
			"exemption every notification is rejected, retried to exhaustion and marked "+
			"exhausted — revocation quietly stays at poll speed.", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
}

// TestCSRFBackchannelExemptionIsNarrow keeps the exemption from becoming a bypass.
//
// ⚠️ The failure guarded against is a predicate that is too loose — a Contains,
// or a prefix swallowing the /auth/sso/ tree. That hands a CSRF bypass to real
// endpoints while every back-channel test still passes.
func TestCSRFBackchannelExemptionIsNarrow(t *testing.T) {
	handler := CSRFMiddleware(dummyHandler())

	for _, path := range []string{
		"/auth/sso/backchannel-logout/extra",
		"/auth/sso/backchannel-logout-something",
		"/auth/sso/config",
		"/containers",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code == http.StatusOK {
				t.Errorf("POST %s passed CSRF with no cookie and no token — the back-channel "+
					"exemption is too broad and is now a CSRF bypass", path)
			}
		})
	}
}
