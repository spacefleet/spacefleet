// Package secrets provides envelope encryption for credentials stored at rest
// (e.g. a registered cluster's kubeconfig or bearer token). A Sealer wraps an
// AES-256-GCM cipher keyed from a single application secret. Ciphertext is
// self-describing — the random nonce is prepended — so Open needs only the key.
//
// When the application has no key configured, NewSealer returns a *disabled*
// Sealer whose Seal/Open both error. Callers that never store secrets (e.g.
// in-cluster cluster registration) are unaffected; anything that needs to seal
// a credential fails fast with a clear, actionable message instead of silently
// persisting plaintext.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrDisabled is returned by a Sealer's Seal/Open when no key is configured.
var ErrDisabled = errors.New("secrets: no encryption key configured (set SPACEFLEET_SECRET_KEY to a base64-encoded 32-byte key)")

// keyLen is the AES-256 key size in bytes.
const keyLen = 32

// Sealer seals and opens credential blobs. The zero value is not usable; build
// one with NewSealer.
type Sealer struct {
	// gcm is nil when the Sealer is disabled (no key configured).
	gcm cipher.AEAD
}

// NewSealer builds a Sealer from a base64-encoded 32-byte key. An empty key
// yields a disabled Sealer (Seal/Open return ErrDisabled) — this is the
// supported "no secrets configured" mode, not an error. A non-empty key that
// doesn't decode to exactly 32 bytes is a misconfiguration and returns an error.
func NewSealer(base64Key string) (*Sealer, error) {
	if base64Key == "" {
		return &Sealer{}, nil
	}
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("secrets: decode key: %w", err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	return &Sealer{gcm: gcm}, nil
}

// Enabled reports whether the Sealer can seal/open (i.e. a key is configured).
func (s *Sealer) Enabled() bool { return s.gcm != nil }

// Seal encrypts plaintext, returning nonce-prefixed ciphertext.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	if s.gcm == nil {
		return nil, ErrDisabled
	}
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secrets: read nonce: %w", err)
	}
	// Seal appends the ciphertext to its first arg (the nonce), so the result
	// is nonce || ciphertext — exactly what Open expects.
	return s.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts nonce-prefixed ciphertext produced by Seal.
func (s *Sealer) Open(ciphertext []byte) ([]byte, error) {
	if s.gcm == nil {
		return nil, ErrDisabled
	}
	ns := s.gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, body := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := s.gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: open: %w", err)
	}
	return plaintext, nil
}
