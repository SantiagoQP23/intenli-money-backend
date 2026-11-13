package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword("correct horse battery", hash); err != nil || !ok {
		t.Fatalf("right password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := CheckPassword("wrong", hash); ok {
		t.Fatal("wrong password accepted")
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Fatal("hashes must be salted")
	}
	if _, err := CheckPassword("x", "$bcrypt$nope"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("expected ErrInvalidHash, got %v", err)
	}
}

func TestAccessTokens(t *testing.T) {
	now := time.Now()
	a := NewAccessTokens([]byte("01234567890123456789012345678901"), 15*time.Minute)
	a.now = func() time.Time { return now }

	id := uuid.New()
	tok, err := a.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Verify(tok)
	if err != nil || got != id {
		t.Fatalf("verify: got %v, %v", got, err)
	}

	// Expired.
	a.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := a.Verify(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token accepted: %v", err)
	}

	// Wrong secret.
	other := NewAccessTokens([]byte("another-secret-another-secret-xx"), time.Minute)
	if _, err := other.Verify(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token from another secret accepted: %v", err)
	}
}

func TestOpaqueToken(t *testing.T) {
	tok, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 || string(HashOpaqueToken(tok)) != string(hash) {
		t.Fatalf("unexpected token/hash: %q", tok)
	}
}
