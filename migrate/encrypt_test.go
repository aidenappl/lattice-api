package migrate

import (
	"os"
	"testing"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/env"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	os.Setenv("DATABASE_DSN", "test:test@tcp(localhost)/test")
	os.Setenv("JWT_SIGNING_KEY", "test-jwt-signing-key-for-unit-tests-minimum-32-chars")
	env.Environment = "development"
	os.Exit(m.Run())
}

func TestNeedsEncryption(t *testing.T) {
	env.EncryptionKey = testEncryptionKey
	crypto.Init()
	if !crypto.IsConfigured() {
		t.Fatal("crypto should be active with a valid key")
	}
	t.Cleanup(func() { env.EncryptionKey = "" })

	// Empty is a no-op.
	if needsEncryption("") {
		t.Error("empty value should not need encryption")
	}

	// A plaintext secret must be flagged for encryption.
	if !needsEncryption("plaintext-registry-password") {
		t.Error("plaintext value should need encryption")
	}

	// A value already produced by Encrypt must be left alone (idempotency).
	enc, err := crypto.Encrypt("some-secret-value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if needsEncryption(enc) {
		t.Error("already-encrypted value should not be re-encrypted")
	}
}

// TestNeedsEncryption_InactivePassthrough documents the dangerous case the
// RunEncrypt guard protects against: with no key, Decrypt is passthrough, so
// every value would be reported as already-encrypted.
func TestNeedsEncryption_InactivePassthrough(t *testing.T) {
	env.EncryptionKey = ""
	crypto.Init()
	if crypto.IsConfigured() {
		t.Fatal("crypto should be inactive without a key")
	}
	if needsEncryption("obviously-plaintext") {
		t.Error("passthrough mode reports plaintext as already-encrypted — RunEncrypt must refuse to run in this state")
	}
}
