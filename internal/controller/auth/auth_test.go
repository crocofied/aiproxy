package auth

import (
	"errors"
	"strings"
	"testing"
)

func init() { Cost = 4 } // the minimum: keeps the tests fast

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Error("hash contains the password")
	}
	if !CheckPassword(hash, "correct horse") {
		t.Error("correct password rejected")
	}
	if CheckPassword(hash, "wrong horse") {
		t.Error("wrong password accepted")
	}
	// Salting: the same password hashes differently every time.
	other, _ := HashPassword("correct horse")
	if other == hash {
		t.Error("two hashes of the same password are identical")
	}
}

func TestPasswordLength(t *testing.T) {
	for _, pw := range []string{"", "short", strings.Repeat("x", 73)} {
		if _, err := HashPassword(pw); !errors.Is(err, ErrPasswordLength) {
			t.Errorf("HashPassword(%d bytes) error = %v, want ErrPasswordLength", len(pw), err)
		}
	}
}

func TestSecrets(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Error("two secrets are identical")
	}
	if len(a) != 43 { // 32 bytes in unpadded base64
		t.Errorf("secret length = %d, want 43", len(a))
	}
	hash := HashSecret(a)
	if !SecretMatches(a, hash) {
		t.Error("secret does not match its own hash")
	}
	if SecretMatches(b, hash) {
		t.Error("different secret matches")
	}
}

func TestAgentToken(t *testing.T) {
	token := FormatAgentToken(7, "abc.def") // a dot inside the secret is fine
	id, secret, err := ParseAgentToken(token)
	if err != nil || id != 7 || secret != "abc.def" {
		t.Errorf("ParseAgentToken(%q) = %d, %q, %v", token, id, secret, err)
	}
	for _, bad := range []string{"", "abc", "7.", ".abc", "x.abc", "0.abc", "-1.abc"} {
		if _, _, err := ParseAgentToken(bad); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("ParseAgentToken(%q) error = %v, want ErrMalformedToken", bad, err)
		}
	}
}
