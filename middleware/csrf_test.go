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
