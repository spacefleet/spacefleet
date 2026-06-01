//go:build integration

package clusters

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/k8s"
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

// TestCreateSealsCredentialsAndProbes registers a token-method cluster and
// confirms the credential is encrypted at rest (and decrypts back), and that
// the unreachable endpoint is recorded as an error status rather than failing
// the call.
func TestCreateSealsCredentialsAndProbes(t *testing.T) {
	client := testsupport.NewEntClient(t)
	sealer := newSealer(t)
	svc := NewService(client, sealer)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	token := []byte("super-secret-sa-token")
	c, err := svc.Create(ctx, org.ID, CreateParams{
		Name:   "prod",
		Method: k8s.MethodToken,
		ConnectionInput: ConnectionInput{
			Endpoint:    "https://10.255.255.1:6443", // unroutable → probe fails fast-ish
			Config:      map[string]string{"insecure_skip_tls": "true"},
			Credentials: token,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if c.EncryptedCredentials == nil {
		t.Fatal("expected credentials to be sealed")
	}
	if bytes.Contains(*c.EncryptedCredentials, token) {
		t.Fatal("stored credentials contain plaintext token")
	}
	opened, err := svc.openCreds(c)
	if err != nil {
		t.Fatalf("openCreds: %v", err)
	}
	if !bytes.Equal(opened, token) {
		t.Fatalf("decrypted creds mismatch: %q", opened)
	}

	// The endpoint is unreachable, so the probe should have recorded an error.
	if c.Status != "error" {
		t.Errorf("status = %q, want error", c.Status)
	}
	if c.LastCheckedAt == nil {
		t.Error("expected last_checked_at to be set after probe")
	}
}

// TestInClusterNeedsNoSecret confirms in_cluster registration works with a
// disabled sealer (no key configured) and stores no credential blob.
func TestInClusterNeedsNoSecret(t *testing.T) {
	client := testsupport.NewEntClient(t)
	disabled, _ := secrets.NewSealer("") // no key
	svc := NewService(client, disabled)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	c, err := svc.Create(ctx, org.ID, CreateParams{
		Name:   "host",
		Method: k8s.MethodInCluster,
	})
	if err != nil {
		t.Fatalf("create in_cluster: %v", err)
	}
	if c.EncryptedCredentials != nil && len(*c.EncryptedCredentials) > 0 {
		t.Error("in_cluster should store no credentials")
	}
	// Probe fails outside k8s, but the row is created and marked.
	if c.Status != "error" {
		t.Errorf("status = %q, want error (no in-cluster config in tests)", c.Status)
	}
}

// TestExternalMethodRequiresKey confirms registering a credential-bearing
// cluster with no encryption key fails fast.
func TestExternalMethodRequiresKey(t *testing.T) {
	client := testsupport.NewEntClient(t)
	disabled, _ := secrets.NewSealer("")
	svc := NewService(client, disabled)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	_, err := svc.Create(ctx, org.ID, CreateParams{
		Name:   "prod",
		Method: k8s.MethodToken,
		ConnectionInput: ConnectionInput{
			Endpoint:    "https://x:6443",
			Credentials: []byte("tok"),
		},
	})
	if err == nil {
		t.Fatal("expected error sealing without a key")
	}
}

// TestOrgScoping confirms clusters are isolated per organization.
func TestOrgScoping(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	orgA := newOrg(t, client, "A")
	orgB := newOrg(t, client, "B")

	c, err := svc.Create(ctx, orgA.ID, CreateParams{Name: "host", Method: k8s.MethodInCluster})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Org B can't see or fetch org A's cluster.
	if list, err := svc.List(ctx, orgB.ID); err != nil || len(list) != 0 {
		t.Fatalf("org B list = %v (err %v), want empty", list, err)
	}
	if _, err := svc.Get(ctx, orgB.ID, c.ID); !ent.IsNotFound(err) {
		t.Fatalf("org B get: got %v, want NotFound", err)
	}
	if err := svc.Delete(ctx, orgB.ID, c.ID); !ent.IsNotFound(err) {
		t.Fatalf("org B delete: got %v, want NotFound", err)
	}

	// Org A still has it; delete works and is final.
	if err := svc.Delete(ctx, orgA.ID, c.ID); err != nil {
		t.Fatalf("org A delete: %v", err)
	}
	if list, _ := svc.List(ctx, orgA.ID); len(list) != 0 {
		t.Fatalf("org A list after delete = %d, want 0", len(list))
	}
}

// TestUniqueNamePerOrg confirms the (organization_id, name) unique index.
func TestUniqueNamePerOrg(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, newSealer(t))
	ctx := context.Background()
	org := newOrg(t, client, "Acme")

	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "dupe", Method: k8s.MethodInCluster}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "dupe", Method: k8s.MethodInCluster}); err == nil {
		t.Fatal("expected unique-violation on duplicate name in same org")
	}
}
