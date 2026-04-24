package tools

import (
	"encoding/hex"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	plaintext, hash, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() unexpected error: %v", err)
	}

	// 48 bytes = 96 hex chars
	if len(plaintext) != 96 {
		t.Errorf("plaintext length = %d, want 96", len(plaintext))
	}

	// SHA-256 hash = 64 hex chars
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash))
	}

	// Plaintext should be valid hex
	if _, err := hex.DecodeString(plaintext); err != nil {
		t.Errorf("plaintext is not valid hex: %v", err)
	}

	// Hash should be valid hex
	if _, err := hex.DecodeString(hash); err != nil {
		t.Errorf("hash is not valid hex: %v", err)
	}
}

func TestHashTokenDeterministic(t *testing.T) {
	input := "test-token-value"
	h1 := HashToken(input)
	h2 := HashToken(input)
	if h1 != h2 {
		t.Errorf("HashToken is not deterministic: %q != %q", h1, h2)
	}
}

func TestHashTokenMatchesGenerate(t *testing.T) {
	plaintext, hash, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() unexpected error: %v", err)
	}
	if HashToken(plaintext) != hash {
		t.Error("HashToken(plaintext) does not match hash from GenerateToken")
	}
}

func TestGenerateTokenUniqueness(t *testing.T) {
	p1, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() unexpected error: %v", err)
	}
	p2, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() unexpected error: %v", err)
	}
	if p1 == p2 {
		t.Error("two GenerateToken calls produced the same plaintext")
	}
}
