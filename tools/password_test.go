package tools

import "testing"

func TestHashPassword(t *testing.T) {
	password := "mysecurepassword"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword(%q) unexpected error: %v", password, err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty string")
	}
	if hash == password {
		t.Fatal("HashPassword returned the plaintext password")
	}
}

func TestCheckPassword(t *testing.T) {
	password := "mysecurepassword"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword(%q) unexpected error: %v", password, err)
	}

	if !CheckPassword(hash, password) {
		t.Error("CheckPassword returned false for correct password")
	}
	if CheckPassword(hash, "wrongpassword") {
		t.Error("CheckPassword returned true for wrong password")
	}
}

func TestHashPasswordDifferentInputs(t *testing.T) {
	hash1, err := HashPassword("password1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hash2, err := HashPassword("password2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash1 == hash2 {
		t.Error("different passwords produced the same hash")
	}
}
