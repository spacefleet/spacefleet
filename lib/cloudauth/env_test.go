package cloudauth

import (
	"context"
	"reflect"
	"testing"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
)

// TestEnv_GCP: the service-account key goes to the secret map as
// GOOGLE_CREDENTIALS and only the project to the plain map; a missing key is
// an error.
func TestEnv_GCP(t *testing.T) {
	t.Parallel()
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderGcp,
		Config:   map[string]string{cloudcredentials.ConfigKeyGCPProject: "acme-prod"},
		Secrets:  map[string]string{cloudcredentials.CredKeyGCPServiceKey: `{"type":"service_account"}`},
	}
	secret, plain, err := Env(context.Background(), r)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	if want := map[string]string{"GOOGLE_CREDENTIALS": `{"type":"service_account"}`}; !reflect.DeepEqual(secret, want) {
		t.Errorf("secret = %v, want %v", secret, want)
	}
	if want := map[string]string{"GOOGLE_PROJECT": "acme-prod", "CLOUDSDK_CORE_PROJECT": "acme-prod"}; !reflect.DeepEqual(plain, want) {
		t.Errorf("plain = %v, want %v", plain, want)
	}
	r.Secrets = nil
	if _, _, err := Env(context.Background(), r); err == nil {
		t.Error("missing service account key must error")
	}
}

// TestEnv_Azure: the client secret is the only secret; the ids are plain.
func TestEnv_Azure(t *testing.T) {
	t.Parallel()
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAzure,
		Config: map[string]string{
			cloudcredentials.ConfigKeyAzureTenantID:     "t",
			cloudcredentials.ConfigKeyAzureClientID:     "c",
			cloudcredentials.ConfigKeyAzureSubscription: "s",
		},
		Secrets: map[string]string{cloudcredentials.CredKeyAzureClientSecret: "shh"},
	}
	secret, plain, err := Env(context.Background(), r)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	if want := map[string]string{"ARM_CLIENT_SECRET": "shh"}; !reflect.DeepEqual(secret, want) {
		t.Errorf("secret = %v, want %v", secret, want)
	}
	if want := map[string]string{"ARM_CLIENT_ID": "c", "ARM_TENANT_ID": "t", "ARM_SUBSCRIPTION_ID": "s"}; !reflect.DeepEqual(plain, want) {
		t.Errorf("plain = %v, want %v", plain, want)
	}
	delete(r.Config, cloudcredentials.ConfigKeyAzureClientID)
	if _, _, err := Env(context.Background(), r); err == nil {
		t.Error("missing client id must error")
	}
}

// TestEnv_AWS: the aws branch is AWSEnv plus the region on the plain map.
func TestEnv_AWS(t *testing.T) {
	t.Parallel()
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "eu-west-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIA",
			cloudcredentials.CredKeyAWSSecretKey:   "s",
		},
	}
	secret, plain, err := Env(context.Background(), r)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	if secret["AWS_ACCESS_KEY_ID"] != "AKIA" || plain["AWS_REGION"] != "eu-west-1" || len(plain) != 1 {
		t.Errorf("secret = %v plain = %v", secret, plain)
	}
	if _, _, err := Env(context.Background(), cloudcredentials.Resolved{Provider: "other"}); err == nil {
		t.Error("unknown provider must error")
	}
}
