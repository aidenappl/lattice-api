package middleware

import (
	"net/http/httptest"
	"testing"
)

// withTrustedProxies swaps the parsed TRUSTED_PROXIES list for one test.
func withTrustedProxies(t *testing.T, raw string) {
	t.Helper()
	prev := trustedProxyNets
	trustedProxyNets = parseTrustedProxies(raw)
	t.Cleanup(func() { trustedProxyNets = prev })
}

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

func TestClientIPBehindTrustedProxy(t *testing.T) {
	withTrustedProxies(t, "10.15.10.71")

	tests := []struct {
		name   string
		peer   string
		xff    string
		realIP string
		want   string
	}{
		{
			// The proxy appends the address it saw; a client-supplied
			// X-Real-IP must not override that.
			name:   "spoofed X-Real-IP loses to the proxy's XFF entry",
			peer:   "10.15.10.71:443",
			xff:    "198.51.100.9, 10.50.50.3",
			realIP: "203.0.113.7",
			want:   "10.50.50.3",
		},
		{
			name: "rightmost XFF entry is used",
			peer: "10.15.10.71:443",
			xff:  "10.50.50.3",
			want: "10.50.50.3",
		},
		{
			name:   "X-Real-IP is the fallback when there is no XFF",
			peer:   "10.15.10.71:443",
			realIP: "10.50.50.3",
			want:   "10.50.50.3",
		},
		{
			name: "no forwarding headers falls back to the peer",
			peer: "10.15.10.71:443",
			want: "10.15.10.71",
		},
		{
			name:   "untrusted peer ignores every header",
			peer:   "192.0.2.1:5000",
			xff:    "10.50.50.3",
			realIP: "203.0.113.7",
			want:   "192.0.2.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.peer
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.realIP != "" {
				r.Header.Set("X-Real-IP", tt.realIP)
			}
			if got := ClientIP(r); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
