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
	// Force non-production so the empty-key passthrough path is exercised rather
	// than panicking (Init panics on an empty ENCRYPTION_KEY in production).
	env.Environment = "development"
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

func TestDecryptNonBase64Errors(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	// With a key configured, a non-base64 value is not a valid ciphertext and
	// must surface an error rather than being silently returned as "plaintext".
	input := "not-base64-!!!@@@"
	if _, err := Decrypt(input); err == nil {
		t.Errorf("Decrypt of non-base64 should error when encryption is active")
	}
}

func TestDecryptTooShortErrors(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	// Valid base64 but too short to contain a nonce — must error, not passthrough.
	input := base64.StdEncoding.EncodeToString([]byte("ab"))
	if _, err := Decrypt(input); err == nil {
		t.Errorf("Decrypt of too-short data should error when encryption is active")
	}
}

func TestDecryptTamperedErrors(t *testing.T) {
	setupEncryption(t)
	defer func() { env.EncryptionKey = ""; active = false }()

	encrypted, err := Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	// Flip the last base64 char to corrupt the ciphertext/tag.
	tampered := encrypted[:len(encrypted)-1]
	if encrypted[len(encrypted)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}
	if _, err := Decrypt(tampered); err == nil {
		t.Errorf("Decrypt of tampered ciphertext should fail authentication")
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
