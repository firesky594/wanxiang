package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashesAreSaltedAndVerify(t *testing.T) {
	first, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword first: %v", err)
	}
	second, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword second: %v", err)
	}
	if first == second {
		t.Fatalf("password hashes must use random salts")
	}
	if !strings.HasPrefix(first, "pbkdf2-sha256$") {
		t.Fatalf("unexpected format: %q", first)
	}
	valid, err := VerifyPassword("secret123", first)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !valid {
		t.Fatalf("correct password rejected")
	}
	valid, err = VerifyPassword("wrong", first)
	if err != nil {
		t.Fatalf("VerifyPassword wrong: %v", err)
	}
	if valid {
		t.Fatalf("wrong password accepted")
	}
}

func TestVerifyPasswordAcceptsLegacySHA256(t *testing.T) {
	legacy := HashSecret("legacy-password")
	valid, err := VerifyPassword("legacy-password", legacy)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !valid {
		t.Fatalf("legacy password rejected")
	}
	if !PasswordNeedsRehash(legacy) {
		t.Fatalf("legacy password should require rehash")
	}
}
