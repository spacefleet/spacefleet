package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// fakeConns returns a static token-method Connection for any cluster id so the
// resolver's kubeconfig step succeeds without a real cluster. The endpoint is a
// public host (the default endpoint policy rejects private/loopback).
type fakeConns struct{}

func (fakeConns) ConnForTekton(_ context.Context, _, _ uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{
		Method:      k8s.MethodToken,
		Endpoint:    "https://api.example.com",
		Credentials: []byte("a-bearer-token"),
	}, nil
}

// fakeCloudCreds returns a known resolved AWS credential.
type fakeCloudCreds struct {
	resolved cloudcredentials.Resolved
	err      error
}

func (f fakeCloudCreds) Resolve(_ context.Context, _, _ uuid.UUID) (cloudcredentials.Resolved, error) {
	if f.err != nil {
		return cloudcredentials.Resolved{}, f.err
	}
	return f.resolved, nil
}

// fakeVars returns a fixed plain/secret split for any scope.
type fakeVars struct {
	plain  map[string]string
	secret map[string]string
}

func (f fakeVars) ResolveEnv(_ context.Context, _, _, _ uuid.UUID) (map[string]string, map[string]string, error) {
	return f.plain, f.secret, nil
}

// TestResolve_Variables confirms variables are injected: non-secret into Env,
// sensitive into SecretEnv, and the two maps are kept disjoint (a name present
// in Env is removed from SecretEnv).
func TestResolve_Variables(t *testing.T) {
	t.Parallel()

	vars := fakeVars{
		plain:  map[string]string{"FOO": "bar"},
		secret: map[string]string{"TOKEN": "s3cret", "FOO": "should-be-dropped"},
	}
	r := NewResolver(fakeConns{}, nil, nil, nil, vars)

	clusterID := uuid.New()
	out, err := r.Resolve(context.Background(), RunInputs{
		OrgID:           uuid.New(),
		ApplicationID:   uuid.New(),
		ComponentID:     uuid.New(),
		RunnerClusterID: clusterID,
		TargetClusterID: clusterID,
		PullsChart:      true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", out.Env["FOO"])
	}
	if out.SecretEnv["TOKEN"] != "s3cret" {
		t.Errorf("SecretEnv[TOKEN] = %q, want s3cret", out.SecretEnv["TOKEN"])
	}
	// FOO is in Env, so it must not also be in SecretEnv.
	if _, ok := out.SecretEnv["FOO"]; ok {
		t.Errorf("FOO present in both Env and SecretEnv; Env should win")
	}
}

// TestResolve_VariablesNilResolver confirms a nil variable resolver injects
// nothing (the feature degrades cleanly).
func TestResolve_VariablesNilResolver(t *testing.T) {
	t.Parallel()
	r := NewResolver(fakeConns{}, nil, nil, nil, nil)
	clusterID := uuid.New()
	out, err := r.Resolve(context.Background(), RunInputs{
		OrgID:           uuid.New(),
		ApplicationID:   uuid.New(),
		RunnerClusterID: clusterID,
		TargetClusterID: clusterID,
		PullsChart:      true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(out.Env) != 0 || len(out.SecretEnv) != 0 {
		t.Errorf("nil variable resolver injected env: %v / %v", out.Env, out.SecretEnv)
	}
}

func TestResolve_CloudCredential(t *testing.T) {
	t.Parallel()

	cloud := fakeCloudCreds{resolved: cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "us-east-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIAEXAMPLE",
			cloudcredentials.CredKeyAWSSecretKey:   "s3cret/with'quote",
		},
	}}

	r := NewResolver(fakeConns{}, nil, nil, cloud, nil)

	clusterID := uuid.New()
	out, err := r.Resolve(context.Background(), RunInputs{
		OrgID:             uuid.New(),
		RunnerClusterID:   clusterID,
		TargetClusterID:   clusterID,
		CloudCredentialID: uuid.New(),
		PullsChart:        true,
	})
	if err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}

	if !out.HasCloudAuth {
		t.Error("HasCloudAuth = false, want true")
	}

	envFile := out.Files[tofu.AWSEnvFile]
	if envFile == "" {
		t.Fatalf("Files[%q] is empty", tofu.AWSEnvFile)
	}
	if !strings.Contains(envFile, "export AWS_ACCESS_KEY_ID='AKIAEXAMPLE'\n") {
		t.Errorf("env file missing access key line:\n%s", envFile)
	}
	// The embedded single quote in the secret must be shell-escaped as '\''.
	if !strings.Contains(envFile, `export AWS_SECRET_ACCESS_KEY='s3cret/with'\''quote'`) {
		t.Errorf("env file missing/incorrectly-escaped secret key line:\n%s", envFile)
	}
	// Sorted key order: access key before secret key.
	if i, j := strings.Index(envFile, "AWS_ACCESS_KEY_ID"), strings.Index(envFile, "AWS_SECRET_ACCESS_KEY"); i < 0 || j < 0 || i > j {
		t.Errorf("env file keys not in sorted order:\n%s", envFile)
	}

	// Region (non-secret) is routed to Env.
	if got := out.Env["AWS_REGION"]; got != "us-east-1" {
		t.Errorf("Env[AWS_REGION] = %q, want us-east-1", got)
	}
	// SECURITY: the secret keys must NEVER appear in out.Env.
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if _, ok := out.Env[k]; ok {
			t.Errorf("secret key %q leaked into out.Env", k)
		}
	}
}

func TestResolve_CloudCredential_NotConfigured(t *testing.T) {
	t.Parallel()

	// cloudCreds is nil but a credential is referenced → clear error.
	r := NewResolver(fakeConns{}, nil, nil, nil, nil)

	clusterID := uuid.New()
	_, err := r.Resolve(context.Background(), RunInputs{
		OrgID:             uuid.New(),
		RunnerClusterID:   clusterID,
		TargetClusterID:   clusterID,
		CloudCredentialID: uuid.New(),
		PullsChart:        true,
	})
	if err == nil {
		t.Fatal("expected an error when a cloud credential is referenced but the service is nil")
	}
	if !strings.Contains(err.Error(), "cloud-credentials service is not configured") {
		t.Errorf("error = %q, want it to mention the unconfigured service", err)
	}
}

func TestResolve_NoCloudCredential(t *testing.T) {
	t.Parallel()

	// No CloudCredentialID set → no cloud auth, no env file, even with a nil
	// cloudCreds resolver.
	r := NewResolver(fakeConns{}, nil, nil, nil, nil)

	clusterID := uuid.New()
	out, err := r.Resolve(context.Background(), RunInputs{
		OrgID:           uuid.New(),
		RunnerClusterID: clusterID,
		TargetClusterID: clusterID,
		PullsChart:      true,
	})
	if err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}
	if out.HasCloudAuth {
		t.Error("HasCloudAuth = true, want false")
	}
	if _, ok := out.Files[tofu.AWSEnvFile]; ok {
		t.Errorf("Files[%q] present when no cloud credential set", tofu.AWSEnvFile)
	}
}
