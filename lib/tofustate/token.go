package tofustate

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Token scopes. A plan unit runs arbitrary module code, so it gets a token
// that can read and lock state but never write it; apply, destroy, and
// state-operation units get write.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// tokenKeyLabel is the HKDF info string the token key is derived under, so
// the signing key is never the sealer's key bytes themselves.
const tokenKeyLabel = "spacefleet tofu state token v1"

// Claims is what a state token asserts: which component workspace's state
// it is for, which execution step it was minted for, what it may do, and
// until when. The state row is looked up from these, never from the
// request path alone.
type Claims struct {
	OrgID          uuid.UUID `json:"org"`
	ApplicationID  uuid.UUID `json:"app"`
	ComponentID    uuid.UUID `json:"cmp"`
	Workspace      string    `json:"ws"`
	WorkflowRunID  uuid.UUID `json:"run"`
	ComponentRunID uuid.UUID `json:"cr"`
	Scope          string    `json:"scope"`
	// Expiry is a Unix time in seconds.
	Expiry int64 `json:"exp"`
}

// CanWrite reports whether the claims allow writing state.
func (c Claims) CanWrite() bool { return c.Scope == ScopeWrite }

// Signer mints and verifies state tokens: base64url(claims JSON) "."
// base64url(HMAC-SHA256). Tokens are stateless — nothing is stored — and
// the real revocation is the liveness check in Service.Authenticate. The
// zero value is not usable; build one with NewSigner.
type Signer struct {
	key []byte
}

// ErrNoKey is returned by NewSigner when no secret key is configured.
var ErrNoKey = errors.New("tofustate: managed state needs SPACEFLEET_SECRET_KEY")

// NewSigner derives the token key from the deployment's base64-encoded
// secret key (SPACEFLEET_SECRET_KEY). An empty key returns ErrNoKey; callers
// treat that as "managed state is off".
func NewSigner(secretKey string) (*Signer, error) {
	if secretKey == "" {
		return nil, ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return nil, fmt.Errorf("tofustate: decode secret key: %w", err)
	}
	key, err := hkdf.Key(sha256.New, raw, nil, tokenKeyLabel, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("tofustate: derive token key: %w", err)
	}
	return &Signer{key: key}, nil
}

// Sign mints a token for the claims.
func (s *Signer) Sign(c Claims) (string, error) {
	if c.Scope != ScopeRead && c.Scope != ScopeWrite {
		return "", fmt.Errorf("tofustate: unknown token scope %q", c.Scope)
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.mac(body), nil
}

// ErrInvalidToken is returned for a token that is malformed, forged,
// expired, or no longer backed by a running step.
var ErrInvalidToken = errors.New("tofustate: invalid or expired state token")

// Verify checks a token's signature and expiry and returns its claims.
func (s *Signer) Verify(token string, now time.Time) (Claims, error) {
	body, mac, ok := strings.Cut(token, ".")
	if !ok || body == "" || mac == "" {
		return Claims{}, ErrInvalidToken
	}
	if subtle.ConstantTimeCompare([]byte(mac), []byte(s.mac(body))) != 1 {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if c.Scope != ScopeRead && c.Scope != ScopeWrite {
		return Claims{}, ErrInvalidToken
	}
	if now.Unix() >= c.Expiry {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

func (s *Signer) mac(body string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
