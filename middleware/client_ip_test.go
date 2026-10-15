package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPStripsPortAndIgnoresUntrustedHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.15.10.71:59400"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	// No trusted proxies are configured in tests, so the forwarded header is
	// ignored and only the port is stripped.
	if got := ClientIP(r); got != "10.15.10.71" {
		t.Fatalf("got %q, want 10.15.10.71", got)
	}
}
