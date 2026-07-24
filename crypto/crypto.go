package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aidenappl/lattice-api/env"
)

var (
	gcm    cipher.AEAD
	active bool
)

func Init() {
	keyHex := env.EncryptionKey
	if keyHex == "" {
		// Secrets at rest are only safe with a key. In production, refuse to
		// boot rather than silently persist plaintext. In development we allow
		// passthrough for convenience, but make it loud.
		if env.Environment == "production" {
			panic("ENCRYPTION_KEY is required in production (64 hex chars / 32 bytes); refusing to start with plaintext secret storage")
		}
		fmt.Println("⚠️  WARNING: ENCRYPTION_KEY is not set — secrets will be stored/read as PLAINTEXT (development passthrough). Do NOT run this configuration in production.")
		active = false
		return
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		panic("ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("failed to create AES cipher: " + err.Error())
	}
	gcm, err = cipher.NewGCM(block)
	if err != nil {
		panic("failed to create GCM: " + err.Error())
	}
	active = true
}

func IsConfigured() bool { return active }

func Encrypt(plaintext string) (string, error) {
	if !active {
		return plaintext, nil // passthrough when not configured
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func Decrypt(encoded string) (string, error) {
	if !active {
		return encoded, nil // passthrough (development, no key configured)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decrypt: value is not valid base64: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("decrypt: ciphertext too short (%d < %d)", len(data), nonceSize)
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: authentication failed: %w", err)
	}
	return string(plaintext), nil
}
