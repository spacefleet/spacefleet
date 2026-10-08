package tofustate

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testKey = "nTs1wv/wXlYpVfBAmy1HibIFHUfD0utwKytCJAFwN/A="

func testClaims(now time.Time) Claims {
	return Claims{
		OrgID:          uuid.New(),
		ApplicationID:  uuid.New(),
		ComponentID:    uuid.New(),
		Workspace:      "default",
		WorkflowRunID:  uuid.New(),
		ComponentRunID: uuid.New(),
		Scope:          ScopeRead,
		Expiry:         now.Add(time.Hour).Unix(),
	}
}

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	want := testClaims(now)
	token, err := s.Sign(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(token, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != want {
		t.Errorf("claims = %+v, want %+v", got, want)
	}
}

// TestTokenRejects: a tampered body or signature, an expired token, a token
// signed by another key, and garbage all fail as ErrInvalidToken.
func TestTokenRejects(t *testing.T) {
	t.Parallel()
	s, _ := NewSigner(testKey)
	other, _ := NewSigner(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	now := time.Now()
	c := testClaims(now)
	token, _ := s.Sign(c)
	body, mac, _ := strings.Cut(token, ".")

	write := c
	write.Scope = ScopeWrite
	writeToken, _ := s.Sign(write)
	writeBody, _, _ := strings.Cut(writeToken, ".")

	expired := c
	expired.Expiry = now.Add(-time.Second).Unix()
	expiredToken, _ := s.Sign(expired)

	foreign, _ := other.Sign(c)

	for name, tok := range map[string]string{
		"scope swapped into the body": writeBody + "." + mac,
		"signature truncated":         body + "." + mac[:len(mac)-2],
		"no signature":                body,
		"empty":                       "",
		"expired":                     expiredToken,
		"another key":                 foreign,
		"not base64":                  "!!!." + mac,
	} {
		if _, err := s.Verify(tok, now); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

// TestTokenKeyIsDerived: the token key is HKDF-derived, never the sealer key
// bytes, so a token's MAC can't be recomputed with the raw key.
func TestTokenKeyIsDerived(t *testing.T) {
	t.Parallel()
	s, _ := NewSigner(testKey)
	raw, _ := base64.StdEncoding.DecodeString(testKey)
	if string(s.key) == string(raw) {
		t.Fatal("token key must not be the raw secret key")
	}
}

func TestSignerNeedsKey(t *testing.T) {
	t.Parallel()
	if _, err := NewSigner(""); !errors.Is(err, ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
	if _, err := NewSigner("not base64!"); err == nil {
		t.Error("want an error for a malformed key")
	}
	s, _ := NewSigner(testKey)
	c := testClaims(time.Now())
	c.Scope = "admin"
	if _, err := s.Sign(c); err == nil {
		t.Error("want an error signing an unknown scope")
	}
}

func TestAddress(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("0b6f8b6e-6c47-4a6e-9d0b-1f4f7a3c2b10")
	if got, want := Address("https://sf.example.com/", id, ""), "https://sf.example.com/api/tofu/state/0b6f8b6e-6c47-4a6e-9d0b-1f4f7a3c2b10/default"; got != want {
		t.Errorf("Address = %q, want %q", got, want)
	}
	if got := Address("http://h:8080", id, "prod.eu"); !strings.HasSuffix(got, "/prod.eu") {
		t.Errorf("Address = %q", got)
	}
}
