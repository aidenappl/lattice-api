package tools

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var (
	namePattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name must be 1-128 chars, alphanumeric with ._- allowed, must start with alphanumeric")
	}
	return nil
}

func ValidateEmail(email string) error {
	if !emailPattern.MatchString(email) || utf8.RuneCountInString(email) > 254 {
		return fmt.Errorf("invalid email format")
	}
	return nil
}

func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if utf8.RuneCountInString(password) > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	return nil
}

const MaxYAMLSize = 1 * 1024 * 1024 // 1MB

func ValidateYAMLSize(yaml string) error {
	if len(yaml) > MaxYAMLSize {
		return fmt.Errorf("YAML content exceeds maximum size of 1MB")
	}
	return nil
}

// ValidateExternalURL checks that a URL is a valid HTTPS URL pointing to a
// public (non-internal) host. Use this for webhook URLs, SSO endpoints, and
// any other user-configured outbound URLs to prevent SSRF.
func ValidateExternalURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL is required")
	}

	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow HTTPS (block http://, ftp://, javascript:, data:, etc.)
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("URL must use HTTPS scheme, got %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("URL must include a hostname")
	}

	// Block known dangerous hostnames
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("URL must not point to internal hosts")
	}

	// Resolve and check for private/reserved IP ranges. Fail CLOSED on a
	// resolution error — an unresolvable host is rejected rather than allowed,
	// so a name that can't be vetted is never treated as safe.
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("could not resolve host %q: %w", host, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isDisallowedIP(ip) {
			return fmt.Errorf("URL resolves to a private/internal IP address (%s)", ipStr)
		}
	}

	return nil
}

// isDisallowedIP reports whether an IP is not a safe public destination —
// loopback, private (RFC1918 + fc00::/7), link-local, unspecified, multicast,
// or CGNAT (100.64.0.0/10, RFC 6598, which net.IP.IsPrivate does NOT cover).
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// 100.64.0.0/10 — carrier-grade NAT shared address space.
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// safeDialControl is a net.Dialer Control hook that rejects connections to
// private/reserved IPs. It runs AFTER DNS resolution against the ACTUAL address
// being dialed, so it pins the connection to a vetted IP and defeats
// DNS-rebinding — validation-time and connect-time resolution can no longer
// diverge to reach an internal host.
func safeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q: %w", address, err)
	}
	if isDisallowedIP(net.ParseIP(host)) {
		return fmt.Errorf("blocked connection to non-public IP %s", host)
	}
	return nil
}

// NewSafeHTTPClient returns an *http.Client whose dialer refuses to connect to
// private/reserved IPs. Use it for outbound requests to user- or admin-
// configured URLs (webhooks, registries) to prevent SSRF via DNS rebinding.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: safeDialControl}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: timeout,
			ForceAttemptHTTP2:   true,
		},
	}
}
