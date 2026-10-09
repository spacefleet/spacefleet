package deploy

import (
	"context"
	"errors"
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

// inClusterTargetConns returns an in_cluster-method Connection for one cluster
// id and a portable token-method Connection for every other, so a test can pit
// an in_cluster target against a different runner.
type inClusterTargetConns struct{ targetID uuid.UUID }

func (c inClusterTargetConns) ConnForTekton(_ context.Context, _, id uuid.UUID) (k8s.Connection, error) {
	if id == c.targetID {
		return k8s.Connection{Method: k8s.MethodInCluster}, nil
	}
	return fakeConns{}.ConnForTekton(context.Background(), uuid.Nil, uuid.Nil)
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

// TestResolve_InClusterTargetOnForeignRunner confirms the early, actionable
// failure: a target (or terraform cluster-auth) cluster registered with the
// in_cluster method yields a kubeconfig only usable from inside that same
// cluster, so resolving it against a different runner fails up front instead
// of as a confusing TLS/auth error mid-step in the pod.
func TestResolve_InClusterTargetOnForeignRunner(t *testing.T) {
	t.Parallel()

	targetID := uuid.New()
	r := NewResolver(inClusterTargetConns{targetID: targetID}, nil, nil, nil, nil)
	_, err := r.Resolve(context.Background(), RunInputs{
		OrgID:           uuid.New(),
		RunnerClusterID: uuid.New(),
		TargetClusterID: targetID,
		PullsChart:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "in_cluster") {
		t.Fatalf("expected the in_cluster runner-mismatch error, got %v", err)
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

	envFile := out.Files[tofu.CloudEnvFile]
	if envFile == "" {
		t.Fatalf("Files[%q] is empty", tofu.CloudEnvFile)
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
	if _, ok := out.Files[tofu.CloudEnvFile]; ok {
		t.Errorf("Files[%q] present when no cloud credential set", tofu.CloudEnvFile)
	}
}

// TestResolve_DynamoLockTable confirms the first-party locking wiring: a
// terraform run that names a DynamoDB lock table gets it ensured at resolve
// time, with the run's materialized credential env and the backend's region —
// and an ensure failure fails the resolve loudly.
func TestResolve_DynamoLockTable(t *testing.T) {
	t.Parallel()

	cloud := fakeCloudCreds{resolved: cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "eu-central-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIAEXAMPLE",
			cloudcredentials.CredKeyAWSSecretKey:   "supersecret",
		},
	}}

	r := NewResolver(fakeConns{}, nil, nil, cloud, nil)
	var gotEnv map[string]string
	var gotRegion, gotTable string
	r.ensureLockTable = func(_ context.Context, env map[string]string, region, table string) error {
		gotEnv, gotRegion, gotTable = env, region, table
		return nil
	}

	clusterID := uuid.New()
	_, err := r.Resolve(context.Background(), RunInputs{
		OrgID:              uuid.New(),
		RunnerClusterID:    clusterID,
		CloudCredentialID:  uuid.New(),
		DynamoDBLockTable:  "tf-locks",
		DynamoDBLockRegion: "us-west-2",
		PullsChart:         true,
	})
	if err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}
	if gotTable != "tf-locks" {
		t.Errorf("ensured table %q, want tf-locks", gotTable)
	}
	// The backend's region, not the credential's default, locates the table.
	if gotRegion != "us-west-2" {
		t.Errorf("ensured in region %q, want us-west-2 (the backend region)", gotRegion)
	}
	if gotEnv["AWS_ACCESS_KEY_ID"] != "AKIAEXAMPLE" {
		t.Errorf("ensure ran with env %v, want the run's materialized credential", gotEnv)
	}
}

func TestResolve_DynamoLockTable_EnsureFailureIsFatal(t *testing.T) {
	t.Parallel()

	cloud := fakeCloudCreds{resolved: cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIAEXAMPLE",
			cloudcredentials.CredKeyAWSSecretKey:   "supersecret",
		},
	}}
	r := NewResolver(fakeConns{}, nil, nil, cloud, nil)
	r.ensureLockTable = func(context.Context, map[string]string, string, string) error {
		return errors.New("create denied")
	}

	clusterID := uuid.New()
	_, err := r.Resolve(context.Background(), RunInputs{
		OrgID:             uuid.New(),
		RunnerClusterID:   clusterID,
		CloudCredentialID: uuid.New(),
		DynamoDBLockTable: "tf-locks",
		PullsChart:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "create denied") {
		t.Fatalf("Resolve err = %v, want the ensure failure", err)
	}
}

// TestResolve_DynamoLockTable_NoCredential confirms ensure is skipped for an
// instance-role run (no cloud credential): this process doesn't hold the
// runner pod's identity, so the table must pre-exist and resolve must not
// fail.
func TestResolve_DynamoLockTable_NoCredential(t *testing.T) {
	t.Parallel()

	r := NewResolver(fakeConns{}, nil, nil, nil, nil)
	r.ensureLockTable = func(context.Context, map[string]string, string, string) error {
		t.Error("ensureLockTable must not run without a cloud credential")
		return nil
	}

	clusterID := uuid.New()
	if _, err := r.Resolve(context.Background(), RunInputs{
		OrgID:             uuid.New(),
		RunnerClusterID:   clusterID,
		DynamoDBLockTable: "tf-locks",
		PullsChart:        true,
	}); err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}
}

// TestResolve_ExposeTFVars confirms the TF_VAR_ mapping: every
// resolved variable is also exported under its TF_VAR_ name in the map its
// sensitivity puts it in, an explicit TF_VAR_x (in either map) beats the copy
// derived from x, already-prefixed names are not prefixed twice, the
// credential-derived AWS_REGION is not mapped, and without it (a non-OpenTofu
// component) nothing changes.
func TestResolve_ExposeTFVars(t *testing.T) {
	t.Parallel()

	vars := fakeVars{
		plain:  map[string]string{"region": "eu-west-1", "TF_VAR_already": "x", "count": "2"},
		secret: map[string]string{"db_password": "hunter2", "TF_VAR_count": "explicit"},
	}
	creds := fakeCloudCreds{resolved: cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "us-east-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIAEXAMPLE",
			cloudcredentials.CredKeyAWSSecretKey:   "s3cret",
		},
	}}
	r := NewResolver(fakeConns{}, nil, nil, creds, vars)
	clusterID := uuid.New()
	in := RunInputs{
		OrgID: uuid.New(), ApplicationID: uuid.New(), ComponentID: uuid.New(),
		RunnerClusterID: clusterID, CloudCredentialID: uuid.New(), PullsChart: true,
		ExposeTFVars: true,
	}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantEnv := map[string]string{
		"region": "eu-west-1", "TF_VAR_region": "eu-west-1",
		"TF_VAR_already": "x",
		"count":          "2",
		"AWS_REGION":     "us-east-1",
	}
	for k, v := range wantEnv {
		if out.Env[k] != v {
			t.Errorf("Env[%s] = %q, want %q", k, out.Env[k], v)
		}
	}
	for _, absent := range []string{"TF_VAR_AWS_REGION", "TF_VAR_TF_VAR_already"} {
		if _, ok := out.Env[absent]; ok {
			t.Errorf("Env must not contain %s", absent)
		}
	}
	if out.SecretEnv["db_password"] != "hunter2" || out.SecretEnv["TF_VAR_db_password"] != "hunter2" {
		t.Errorf("sensitive variable must be exported under both names in SecretEnv: %v", out.SecretEnv)
	}
	// The explicit sensitive TF_VAR_count wins over the copy derived from the
	// plain count, and stays sensitive.
	if _, ok := out.Env["TF_VAR_count"]; ok {
		t.Errorf("derived TF_VAR_count must not shadow the explicit sensitive one: %v", out.Env)
	}
	if out.SecretEnv["TF_VAR_count"] != "explicit" {
		t.Errorf("SecretEnv[TF_VAR_count] = %q, want explicit", out.SecretEnv["TF_VAR_count"])
	}

	in.ExposeTFVars = false
	out, err = r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve (not exposed): %v", err)
	}
	if _, ok := out.Env["TF_VAR_region"]; ok {
		t.Errorf("unexposed, no TF_VAR_ copies must be made: %v", out.Env)
	}
}

// TestResolve_GCPCredential: a gcp credential lands its service-account key
// in the mounted cloud env file (never in Env), the project on the pod env,
// and no DynamoDB lock-table ensure happens even when a table is named (that
// is an s3 concern).
func TestResolve_GCPCredential(t *testing.T) {
	t.Parallel()
	cloud := fakeCloudCreds{resolved: cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderGcp,
		Config:   map[string]string{cloudcredentials.ConfigKeyGCPProject: "acme-prod"},
		Secrets:  map[string]string{cloudcredentials.CredKeyGCPServiceKey: "{\"type\":\"service_account\",\n\"private_key\":\"it's secret\"}"},
	}}
	r := NewResolver(fakeConns{}, nil, nil, cloud, nil)
	ensured := false
	r.ensureLockTable = func(context.Context, map[string]string, string, string) error { ensured = true; return nil }
	out, err := r.Resolve(context.Background(), RunInputs{
		OrgID: uuid.New(), RunnerClusterID: uuid.New(), CloudCredentialID: uuid.New(), PullsChart: true,
		DynamoDBLockTable: "tf-locks", DynamoDBLockRegion: "us-east-1",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !out.HasCloudAuth {
		t.Error("HasCloudAuth must be set")
	}
	envFile := out.Files[tofu.CloudEnvFile]
	if !strings.Contains(envFile, "export GOOGLE_CREDENTIALS='{\"type\":\"service_account\",\n\"private_key\":\"it'\\''s secret\"}'\n") {
		t.Errorf("cloud env file = %q", envFile)
	}
	if out.Env["GOOGLE_PROJECT"] != "acme-prod" || out.Env["CLOUDSDK_CORE_PROJECT"] != "acme-prod" {
		t.Errorf("project must be on the pod env: %v", out.Env)
	}
	if _, ok := out.Env["GOOGLE_CREDENTIALS"]; ok {
		t.Error("the service account key must never be on the pod env")
	}
	if ensured {
		t.Error("a DynamoDB lock table must not be ensured for a gcp credential")
	}
}
