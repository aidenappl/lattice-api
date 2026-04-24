package crypto

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/aidenappl/lattice-api/env"
)

// testEncryptionKey is a valid 64-hex-char (32-byte) key for tests.
const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	os.Setenv("DATABASE_DSN", "test:test@tcp(localhost)/test")
	os.Setenv("JWT_SIGNING_KEY", "test-jwt-signing-key-for-unit-tests-minimum-32-chars")
	os.Exit(m.Run())
}

func setupEncryption(t *testing.T) {
	t.Helper()
	env.EncryptionKey = testEncryptionKey
	Init()
	if !IsConfigured() {
		t.Fatal("encryption should be configured after Init with valid key")
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	plaintext := "my-secret-registry-password"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	if encrypted == plaintext {
		t.Error("encrypted should differ from plaintext")
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("Decrypt = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptProducesBase64(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	encrypted, err := Encrypt("test")
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(encrypted); err != nil {
		t.Errorf("encrypted output is not valid base64: %v", err)
	}
}

func TestDecryptNonBase64ReturnsInput(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	input := "not-base64-!!!@@@"
	result, err := Decrypt(input)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if result != input {
		t.Errorf("Decrypt of non-base64 = %q, want %q (migration compat)", result, input)
	}
}

func TestDecryptTooShortReturnsInput(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	// Valid base64 but too short to contain a nonce
	input := base64.StdEncoding.EncodeToString([]byte("ab"))
	result, err := Decrypt(input)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if result != input {
		t.Errorf("Decrypt of too-short data = %q, want %q", result, input)
	}
}

func TestPassthroughWhenNotConfigured(t *testing.T) {
	env.EncryptionKey = ""
	Init()
	defer func() { active = false }()

	if IsConfigured() {
		t.Fatal("should not be configured with empty key")
	}

	plaintext := "my-secret"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	if encrypted != plaintext {
		t.Errorf("Encrypt passthrough = %q, want %q", encrypted, plaintext)
	}

	decrypted, err := Decrypt(plaintext)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("Decrypt passthrough = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDifferentOutputs(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	e1, _ := Encrypt("same-input")
	e2, _ := Encrypt("same-input")
	if e1 == e2 {
		t.Error("two encryptions of same input should produce different ciphertexts (random nonce)")
	}
}
