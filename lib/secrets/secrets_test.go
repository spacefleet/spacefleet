package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("read key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := NewSealer(newKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if !s.Enabled() {
		t.Fatal("expected sealer to be enabled")
	}

	plaintext := []byte("apiVersion: v1\nkind: Config\ntoken: super-secret")
	sealed, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("sealed output contains plaintext")
	}

	opened, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip mismatch: got %q", opened)
	}
}

func TestSealProducesDistinctCiphertext(t *testing.T) {
	s, err := NewSealer(newKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	a, _ := s.Seal([]byte("same"))
	b, _ := s.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("expected distinct ciphertext from random nonces")
	}
}

func TestDisabledSealer(t *testing.T) {
	s, err := NewSealer("")
	if err != nil {
		t.Fatalf("NewSealer(\"\"): %v", err)
	}
	if s.Enabled() {
		t.Fatal("expected disabled sealer")
	}
	if _, err := s.Seal([]byte("x")); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Seal: want ErrDisabled, got %v", err)
	}
	if _, err := s.Open([]byte("x")); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open: want ErrDisabled, got %v", err)
	}
}

func TestNewSealerBadKey(t *testing.T) {
	if _, err := NewSealer("not-base64!!!"); err == nil {
		t.Fatal("expected error for non-base64 key")
	}
	if _, err := NewSealer(base64.StdEncoding.EncodeToString([]byte("too-short"))); err == nil {
		t.Fatal("expected error for wrong-length key")
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	s, err := NewSealer(newKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, _ := s.Seal([]byte("authentic"))
	sealed[len(sealed)-1] ^= 0xff // flip a ciphertext bit
	if _, err := s.Open(sealed); err == nil {
		t.Fatal("expected Open to reject tampered ciphertext")
	}
	if _, err := s.Open([]byte("short")); err == nil {
		t.Fatal("expected Open to reject too-short input")
	}
}
