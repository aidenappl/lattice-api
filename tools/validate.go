package tools

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
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

	// Resolve and check for private/reserved IP ranges
	ips, err := net.LookupHost(host)
	if err != nil {
		// DNS resolution failed — allow it (host may be valid but unreachable from this network)
		return nil
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("URL resolves to a private/internal IP address (%s)", ipStr)
		}
	}

	return nil
}
