//go:build integration

package cloudcredentials

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/testsupport"
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

func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

func awsCreds(t *testing.T, secret string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{CredKeyAWSAccessKeyID: "AKIA", CredKeyAWSSecretKey: secret})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return b
}

// TestCreateSealsAndResolveRoundTrips registers a credential and confirms the
// secret is sealed at rest (not stored in plaintext) but Resolve round-trips
// it, and the stored row is scoped to its organization.
func TestCreateSealsAndResolveRoundTrips(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	c, err := svc.Create(ctx, org.ID, CreateParams{
		Name:        "production-aws",
		Description: "billing",
		Provider:    cloudcredential.ProviderAWS,
		Config:      map[string]string{ConfigKeyAWSRegion: "us-east-1"},
		Credentials: awsCreds(t, "s3cret"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The stored blob must not contain the plaintext secret.
	if c.EncryptedCredentials == nil || string(*c.EncryptedCredentials) == string(awsCreds(t, "s3cret")) {
		t.Fatalf("credentials were not sealed at rest")
	}

	got, err := svc.Resolve(ctx, org.ID, c.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Provider != cloudcredential.ProviderAWS {
		t.Errorf("provider = %q, want aws", got.Provider)
	}
	if got.Secrets[CredKeyAWSSecretKey] != "s3cret" {
		t.Errorf("Resolve secret = %q, want s3cret", got.Secrets[CredKeyAWSSecretKey])
	}
	if got.Config[ConfigKeyAWSRegion] != "us-east-1" {
		t.Errorf("Resolve config region = %q, want us-east-1", got.Config[ConfigKeyAWSRegion])
	}

	// A different org cannot see it.
	other := newOrg(t, client, "Other")
	if _, err := svc.Get(ctx, other.ID, c.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
}

// TestDuplicateProviderAllowed confirms an org may register several credentials
// of the same provider, as long as the names differ.
func TestDuplicateProviderAllowed(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	mk := func(name string) error {
		_, err := svc.Create(ctx, org.ID, CreateParams{
			Name:        name,
			Provider:    cloudcredential.ProviderAWS,
			Credentials: awsCreds(t, "x"),
		})
		return err
	}
	if err := mk("aws-one"); err != nil {
		t.Fatalf("first aws cred: %v", err)
	}
	if err := mk("aws-two"); err != nil {
		t.Fatalf("second aws cred (same provider) should be allowed: %v", err)
	}
	// ...but a duplicate name is a constraint error.
	if err := mk("aws-one"); !ent.IsConstraintError(err) {
		t.Errorf("duplicate name error = %v, want constraint error", err)
	}
}

// TestCreateWithoutKeyDisabled confirms storing a credential without an
// encryption key fails fast with ErrDisabled rather than persisting plaintext.
func TestCreateWithoutKeyDisabled(t *testing.T) {
	client := testsupport.NewEntClient(t)
	disabled, _ := secrets.NewSealer("") // no key
	svc := NewService(client, disabled)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	_, err := svc.Create(ctx, org.ID, CreateParams{
		Name:        "r",
		Provider:    cloudcredential.ProviderAWS,
		Credentials: awsCreds(t, "x"),
	})
	if !errors.Is(err, secrets.ErrDisabled) {
		t.Fatalf("Create error = %v, want ErrDisabled", err)
	}
}

// TestCreateRejectsBadInput confirms missing credentials is a client-input error.
func TestCreateRejectsBadInput(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "r", Provider: cloudcredential.ProviderAWS}); !IsValidation(err) {
		t.Errorf("missing credentials error = %v, want ValidationError", err)
	}
}

// TestUpdateRotatesSecret confirms new credentials re-seal and Resolve returns
// the rotated value, while a nil credential leaves the stored one intact.
func TestUpdateRotatesSecret(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	cred, err := svc.Create(ctx, org.ID, CreateParams{
		Name:        "aws",
		Provider:    cloudcredential.ProviderAWS,
		Credentials: awsCreds(t, "old"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := "aws-prod"
	if _, err := svc.Update(ctx, org.ID, cred.ID, UpdateParams{
		Name:        &newName,
		Credentials: awsCreds(t, "new"),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Resolve(ctx, org.ID, cred.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Secrets[CredKeyAWSSecretKey] != "new" {
		t.Errorf("secret = %q, want rotated 'new'", got.Secrets[CredKeyAWSSecretKey])
	}

	// A metadata-only update leaves the secret intact.
	desc := "renamed"
	if _, err := svc.Update(ctx, org.ID, cred.ID, UpdateParams{Description: &desc}); err != nil {
		t.Fatalf("metadata Update: %v", err)
	}
	got, err = svc.Resolve(ctx, org.ID, cred.ID)
	if err != nil {
		t.Fatalf("Resolve after metadata update: %v", err)
	}
	if got.Secrets[CredKeyAWSSecretKey] != "new" {
		t.Errorf("secret after metadata update = %q, want 'new'", got.Secrets[CredKeyAWSSecretKey])
	}

	// An empty name is rejected.
	empty := ""
	if _, err := svc.Update(ctx, org.ID, cred.ID, UpdateParams{Name: &empty}); !IsValidation(err) {
		t.Errorf("empty name error = %v, want ValidationError", err)
	}
}
