package tools

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "myapp", false},
		{"with-hyphen", "my-app", false},
		{"with-dot", "my.app", false},
		{"with-underscore", "my_app", false},
		{"mixed", "a-b.c_d", false},
		{"single-char", "a", false},
		{"starts-with-digit", "1app", false},
		{"max-length", strings.Repeat("a", 128), false},
		{"empty", "", true},
		{"starts-with-dot", ".app", true},
		{"starts-with-hyphen", "-app", true},
		{"starts-with-underscore", "_app", true},
		{"over-max-length", strings.Repeat("a", 129), true},
		{"has-space", "has space", true},
		{"has-semicolon", "has;semi", true},
		{"has-dollar", "has$dollar", true},
		{"has-ampersand", "has&amp", true},
		{"has-backtick", "has`tick", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "user@example.com", false},
		{"with-plus", "user+tag@example.com", false},
		{"subdomain", "user@sub.domain.com", false},
		{"with-dots", "first.last@example.com", false},
		{"empty", "", true},
		{"no-at", "userexample.com", true},
		{"no-domain", "user@", true},
		{"no-user", "@example.com", true},
		{"no-tld", "user@example", true},
		{"over-254-runes", strings.Repeat("a", 243) + "@example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEmail(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEmail(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"min-length", "12345678", false},
		{"max-length", strings.Repeat("a", 128), false},
		{"normal", "mysecurepassword", false},
		{"too-short", "1234567", true},
		{"too-long", strings.Repeat("a", 129), true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePassword(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateYAMLSize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", false},
		{"small", "key: value", false},
		{"exactly-1MB", strings.Repeat("a", MaxYAMLSize), false},
		{"over-1MB", strings.Repeat("a", MaxYAMLSize+1), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateYAMLSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateYAMLSize(len=%d) error = %v, wantErr %v", len(tt.input), err, tt.wantErr)
			}
		})
	}
}

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid-https", "https://example.com", false},
		{"valid-with-path", "https://example.com/webhook", false},
		{"valid-with-port", "https://example.com:8443/hook", false},
		{"empty", "", true},
		{"http-scheme", "http://example.com", true},
		{"ftp-scheme", "ftp://example.com", true},
		{"no-scheme", "example.com", true},
		{"localhost", "https://localhost", true},
		{"dot-local", "https://foo.local", true},
		{"dot-internal", "https://foo.internal", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExternalURL(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
