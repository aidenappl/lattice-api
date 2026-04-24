package jwt

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/env"
)

func TestMain(m *testing.M) {
	// Set env vars required by env package init
	os.Setenv("DATABASE_DSN", "test:test@tcp(localhost)/test")
	os.Setenv("JWT_SIGNING_KEY", "test-jwt-signing-key-for-unit-tests-minimum-32-chars")
	env.JWTSigningKey = "test-jwt-signing-key-for-unit-tests-minimum-32-chars"
	os.Exit(m.Run())
}

func TestNewAccessToken(t *testing.T) {
	token, expiresAt, err := NewAccessToken(42)
	if err != nil {
		t.Fatalf("NewAccessToken(42) unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("NewAccessToken returned empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expiry should be in the future")
	}
	if expiresAt.After(time.Now().Add(16 * time.Minute)) {
		t.Error("expiry should be within ~15 minutes")
	}
}

func TestNewRefreshToken(t *testing.T) {
	token, expiresAt, err := NewRefreshToken(42)
	if err != nil {
		t.Fatalf("NewRefreshToken(42) unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("NewRefreshToken returned empty token")
	}
	if !expiresAt.After(time.Now().Add(6 * 24 * time.Hour)) {
		t.Error("refresh token expiry should be ~7 days out")
	}
}

func TestValidateAccessToken(t *testing.T) {
	token, _, err := NewAccessToken(42)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}

	userID, err := ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken error: %v", err)
	}
	if userID != 42 {
		t.Errorf("userID = %d, want 42", userID)
	}
}

func TestValidateAccessTokenRejectsRefresh(t *testing.T) {
	token, _, err := NewRefreshToken(42)
	if err != nil {
		t.Fatalf("NewRefreshToken error: %v", err)
	}

	_, err = ValidateAccessToken(token)
	if err == nil {
		t.Error("ValidateAccessToken should reject a refresh token")
	}
}

func TestValidateRefreshToken(t *testing.T) {
	token, _, err := NewRefreshToken(42)
	if err != nil {
		t.Fatalf("NewRefreshToken error: %v", err)
	}

	userID, err := ValidateRefreshToken(token)
	if err != nil {
		t.Fatalf("ValidateRefreshToken error: %v", err)
	}
	if userID != 42 {
		t.Errorf("userID = %d, want 42", userID)
	}
}

func TestValidateRefreshTokenRejectsAccess(t *testing.T) {
	token, _, err := NewAccessToken(42)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}

	_, err = ValidateRefreshToken(token)
	if err == nil {
		t.Error("ValidateRefreshToken should reject an access token")
	}
}

func TestValidateTokenWrongKey(t *testing.T) {
	token, _, err := NewAccessToken(42)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}

	// Change signing key
	original := env.JWTSigningKey
	env.JWTSigningKey = "different-signing-key-for-testing-minimum-32-chars"
	defer func() { env.JWTSigningKey = original }()

	_, err = ValidateToken(token)
	if err == nil {
		t.Error("ValidateToken should reject token signed with different key")
	}
}

func TestValidateTokenTampered(t *testing.T) {
	token, _, err := NewAccessToken(42)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}

	// Flip a character in the signature portion
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	sig := []byte(parts[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	tampered := parts[0] + "." + parts[1] + "." + string(sig)

	_, err = ValidateToken(tampered)
	if err == nil {
		t.Error("ValidateToken should reject tampered token")
	}
}

func TestValidateTokenClaims(t *testing.T) {
	token, _, err := NewAccessToken(99)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}

	claims, err := ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken error: %v", err)
	}
	if claims.Issuer != "lattice" {
		t.Errorf("issuer = %q, want %q", claims.Issuer, "lattice")
	}
	if claims.UserID != 99 {
		t.Errorf("userID = %d, want 99", claims.UserID)
	}
	if claims.Type != "access" {
		t.Errorf("type = %q, want %q", claims.Type, "access")
	}
}
