package tools

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// GenerateToken creates a cryptographically random token string (48 bytes = 64 hex chars)
// and returns both the plaintext token and its SHA-256 hash.
// The plaintext is shown to the user once; only the hash is stored.
func GenerateToken() (plaintext string, hash string, err error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate random token: %w", err)
	}
	plaintext = hex.EncodeToString(b)
	hash = HashToken(plaintext)
	return plaintext, hash, nil
}

// HashToken returns the SHA-256 hex digest of a plaintext token.
func HashToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
