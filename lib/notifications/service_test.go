package notifications

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/spacefleet/spacefleet/lib/secrets"
)

func newSealer(t *testing.T) *secrets.Sealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	s, err := secrets.NewSealer(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return s
}

// TestPrepareTarget covers destination validation per kind: an email must
// be a bare address; a webhook kind must be an http(s) URL, is sealed, and
// shows only its host; a webhook without a sealer reports encryption off.
func TestPrepareTarget(t *testing.T) {
	t.Parallel()
	s := &Service{sealer: newSealer(t)}
	addr, sealed, err := s.prepareTarget(KindEmail, " ops@example.com ")
	if err != nil || addr != "ops@example.com" || sealed != nil {
		t.Errorf("email: addr=%q sealed=%v err=%v", addr, sealed, err)
	}
	for _, bad := range []string{"ops", "Ops <ops@example.com>", "a@b, c@d"} {
		if _, _, err := s.prepareTarget(KindEmail, bad); !IsValidation(err) {
			t.Errorf("email %q: err = %v, want validation", bad, err)
		}
	}
	addr, sealed, err = s.prepareTarget(KindSlack, "https://hooks.slack.com/services/T/B/x")
	if err != nil || addr != "hooks.slack.com" || len(sealed) == 0 {
		t.Errorf("slack: addr=%q sealed=%d err=%v", addr, len(sealed), err)
	}
	raw, err := s.sealer.Open(sealed)
	if err != nil || string(raw) != "https://hooks.slack.com/services/T/B/x" {
		t.Errorf("unsealed = %q err=%v", raw, err)
	}
	for _, bad := range []string{"hooks.slack.com/x", "ftp://x/y", ""} {
		if _, _, err := s.prepareTarget(KindWebhook, bad); !IsValidation(err) {
			t.Errorf("webhook %q: err = %v, want validation", bad, err)
		}
	}
	if _, _, err := s.prepareTarget("pager", "x"); !IsValidation(err) {
		t.Errorf("unknown kind: err = %v", err)
	}
	off := &Service{sealer: mustSealer(t, "")}
	if _, _, err := off.prepareTarget(KindWebhook, "https://example.com/hook"); !errors.Is(err, secrets.ErrDisabled) {
		t.Errorf("no key: err = %v, want ErrDisabled", err)
	}
}

func mustSealer(t *testing.T, key string) *secrets.Sealer {
	t.Helper()
	s, err := secrets.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateEvents(t *testing.T) {
	t.Parallel()
	if err := validateEvents([]string{"run_failed", "drift_detected"}); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, in := range map[string][]string{
		"empty":     {},
		"unknown":   {"deployed"},
		"test kind": {"test"},
		"duplicate": {"run_failed", "run_failed"},
	} {
		if err := validateEvents(in); !IsValidation(err) {
			t.Errorf("%s: err = %v, want validation", name, err)
		}
	}
}
