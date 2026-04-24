package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/env"
)

func TestMain(m *testing.M) {
	os.Setenv("DATABASE_DSN", "test:test@tcp(localhost)/test")
	os.Setenv("JWT_SIGNING_KEY", "test-jwt-signing-key-for-unit-tests-minimum-32-chars")
	os.Exit(m.Run())
}

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRequestIDMiddleware(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	id := rr.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("X-Request-ID header not set")
	}
	if !uuidRegex.MatchString(id) {
		t.Errorf("X-Request-ID %q is not a valid UUID", id)
	}
}

func TestGetRequestIDWithoutContext(t *testing.T) {
	ctx := context.Background()
	id := GetRequestID(ctx)
	if id != "unknown" {
		t.Errorf("GetRequestID = %q, want %q", id, "unknown")
	}
}

func TestGetRequestIDWithContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), RequestIDKey, "test-id-123")
	id := GetRequestID(ctx)
	if id != "test-id-123" {
		t.Errorf("GetRequestID = %q, want %q", id, "test-id-123")
	}
}

func TestLoggingMiddlewareSkipsHealthcheck(t *testing.T) {
	called := false
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler was not called for /healthcheck")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestMuxHeaderMiddleware(t *testing.T) {
	handler := MuxHeaderMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Server") != "Go" {
		t.Errorf("Server header = %q, want %q", rr.Header().Get("Server"), "Go")
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	original := env.Environment
	env.Environment = "development"
	defer func() { env.Environment = original }()

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}
	if rr.Header().Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
		t.Error("missing Referrer-Policy")
	}
	if rr.Header().Get("Permissions-Policy") == "" {
		t.Error("missing Permissions-Policy")
	}
	if rr.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS should not be set in non-production")
	}
}

func TestSecurityHeadersHSTSInProduction(t *testing.T) {
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	original := env.Environment
	env.Environment = "production"
	defer func() { env.Environment = original }()

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS should be set in production")
	}
}

func TestMaxBodySizeSkipsWebSocket(t *testing.T) {
	handler := MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", strings.NewReader("this is a longer body than 10 bytes"))
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("WebSocket request should pass through, got status %d", rr.Code)
	}
}

func TestMaxBodySizeLimitsBody(t *testing.T) {
	handler := MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 100)
		_, err := r.Body.Read(buf)
		if err != nil {
			// MaxBytesReader returns an error when limit exceeded
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	body := strings.Repeat("x", 20)
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rr.Code)
	}
}
