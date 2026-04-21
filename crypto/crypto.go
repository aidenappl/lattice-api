package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
		return encoded, nil // passthrough
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded, nil // assume it's plaintext (migration compatibility)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return encoded, nil // too short to be encrypted, treat as plaintext
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return encoded, nil // decryption failed, assume plaintext (migration)
	}
	return string(plaintext), nil
}
