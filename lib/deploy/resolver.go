// Package deploy holds the one implementation of the resolution every helm run
// needs, regardless of whether it is driven by a single-helm Application
// (lib/applications) or a workflow Component (lib/workflows): resolve the runner
// connection, build the target cluster's kubeconfig (minting any cloud token
// late, per attempt), decrypt any private-chart credential, mint any GitHub App
// installation token, and assemble the Files injected into the rollout step.
//
// This logic used to live (only) in applications.Service.resolveRunInputs; it is
// extracted here so the workflow per-component planner reuses it byte-for-byte
// rather than duplicating the credential / kubeconfig / git-token handling (the
// exact thing the plan calls out as needing a single source of truth). Both
// callers construct a Resolver with the same three dependencies and hand it a
// generic RunInputs describing one helm run; the resolver never touches ent, so
// it stays decoupled from either caller's row type.
package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/cloudauth"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// ConnResolver resolves the decrypted connection for an org-scoped cluster. It is
// the narrow slice of *clusters.Service a run needs; mirrors the seam
// lib/applications defines so the same *clusters.Service satisfies both.
type ConnResolver interface {
	ConnForTekton(ctx context.Context, orgID, clusterID uuid.UUID) (k8s.Connection, error)
}

// CredentialResolver decrypts an org-scoped chart credential. May be nil (no
// chart-credentials service wired), in which case a run referencing a credential
// fails with a clear error.
type CredentialResolver interface {
	Resolve(ctx context.Context, orgID, id uuid.UUID) (chartcredentials.Resolved, error)
}

// GitTokenResolver mints a short-lived GitHub App installation token for an
// org-scoped installation, to authenticate a private-Git clone. May be nil (no
// GitHub App / installations service wired), in which case a run referencing an
// installation fails with a clear error.
type GitTokenResolver interface {
	InstallationToken(ctx context.Context, orgID, id uuid.UUID) (string, error)
}

// CloudCredentialResolver decrypts an org-scoped cloud credential (AWS, etc.),
// for a terraform component that authenticates to the cloud (the state backend
// and the module's providers). May be nil (no cloud-credentials service wired),
// in which case a run referencing a credential fails with a clear error.
type CloudCredentialResolver interface {
	Resolve(ctx context.Context, orgID, id uuid.UUID) (cloudcredentials.Resolved, error)
}

// VariableResolver resolves the merged environment variables for one component
// job: the app-level variables overlaid by the component's own. It returns two
// disjoint maps — non-secret values (plain) and decrypted sensitive values
// (secret). May be nil (no variables service wired), in which case no variables
// are injected.
type VariableResolver interface {
	ResolveEnv(ctx context.Context, orgID, appID, componentID uuid.UUID) (plain map[string]string, secret map[string]string, err error)
}

// Resolver holds the run-time dependencies (connection / credential / git token
// / cloud credential) and is the single implementation of the run input
// resolution. Construct it with the same deps the applications service already
// holds.
type Resolver struct {
	conns      ConnResolver
	creds      CredentialResolver
	gitTokens  GitTokenResolver
	cloudCreds CloudCredentialResolver
	vars       VariableResolver
	// ensureLockTable provisions a missing DynamoDB state-lock table before a
	// run (cloudauth.EnsureLockTable in production); a field so tests exercise
	// the wiring without an AWS call.
	ensureLockTable func(ctx context.Context, env map[string]string, region, table string) error
}

// NewResolver builds a Resolver over the connection, credential, git-token,
// cloud-credential, and variable resolvers. creds/gitTokens/cloudCreds/vars may
// be nil; a run that references a missing credential service fails with a clear
// "not configured" error, and a nil variable resolver simply injects no
// variables.
func NewResolver(conns ConnResolver, creds CredentialResolver, gitTokens GitTokenResolver, cloudCreds CloudCredentialResolver, vars VariableResolver) *Resolver {
	return &Resolver{conns: conns, creds: creds, gitTokens: gitTokens, cloudCreds: cloudCreds, vars: vars, ensureLockTable: cloudauth.EnsureLockTable}
}

// RunInputs is the generic description of one helm run the resolver needs,
// decoupled from any ent row. The caller (applications or workflows) fills it
// from its own model.
type RunInputs struct {
	OrgID uuid.UUID
	// ApplicationID + ComponentID locate the variables to inject as the job's
	// environment (app-level overlaid by the component's own). ComponentID is
	// uuid.Nil for a run with no component scope (app-level variables only).
	ApplicationID uuid.UUID
	ComponentID   uuid.UUID
	// RunnerClusterID is the cluster the TaskRun runs on; TargetClusterID is the
	// cluster the release lands on (they may be the same).
	RunnerClusterID uuid.UUID
	TargetClusterID uuid.UUID
	// Values is the inline values.yaml contents injected as the values file.
	Values string
	// ChartCredentialID / GitHubInstallationID are uuid.Nil when not attached.
	ChartCredentialID    uuid.UUID
	GitHubInstallationID uuid.UUID
	// CloudCredentialID is the org-scoped cloud credential to authenticate a
	// terraform run to the cloud (AWS). uuid.Nil when none.
	CloudCredentialID uuid.UUID
	// DynamoDBLockTable, when set (a terraform component whose s3 backend names
	// a DynamoDB state-lock table), is ensured to exist at resolve time —
	// created with the LockID schema OpenTofu requires when missing — using the
	// run's cloud credential, so naming a table is all a user does to get
	// locking. Ignored when no CloudCredentialID is attached: an instance-role
	// run authenticates as the runner pod, an identity this process doesn't
	// hold, so there the table must pre-exist.
	DynamoDBLockTable string
	// DynamoDBLockRegion is the region the lock table lives in — the s3
	// backend's region (OpenTofu resolves the lock table in the backend region,
	// not the credential's default region).
	DynamoDBLockRegion string
	// PullsChart is false only for uninstall (which pulls nothing), so the chart
	// credential and git token are skipped.
	PullsChart bool
}

// Resolved is what the resolver computes: the runner connection, the target
// connection method (for the wait timeout), the injected Files (kubeconfig +
// values + any credentials), and the auth flags the helm script needs.
type Resolved struct {
	RunnerConn    k8s.Connection
	TargetMethod  k8s.Method
	Files         map[string]string
	HasCredential bool
	HasGitToken   bool
	// HasCloudAuth is set when a cloud credential was resolved and its env file
	// injected (out.Files[tofu.AWSEnvFile]); the terraform script sources it.
	HasCloudAuth bool
	// Env is non-secret environment the step exports (e.g. AWS_REGION and the
	// non-sensitive variables). The planner threads it into RunSpec.Env. Secret
	// credential keys NEVER go here — only into the mounted Files[tofu.AWSEnvFile].
	Env map[string]string
	// SecretEnv is the sensitive variables (decrypted), injected into the step as
	// env vars sourced from the per-run creds Secret (never inline in the TaskRun
	// manifest). The planner threads it into RunSpec.SecretEnv.
	SecretEnv map[string]string
}

// Resolve resolves the runner connection, builds the target cluster's kubeconfig
// (minting any cloud token late, this attempt), decrypts any chart credential,
// mints any GitHub App token, and assembles the injected Files. It is the one
// implementation of this logic; lib/applications and lib/workflows both call it.
func (r *Resolver) Resolve(ctx context.Context, in RunInputs) (Resolved, error) {
	runnerConn, err := r.conns.ConnForTekton(ctx, in.OrgID, in.RunnerClusterID)
	if err != nil {
		return Resolved{}, err
	}

	out := Resolved{
		RunnerConn: runnerConn,
		Files: map[string]string{
			helm.ValuesFile: in.Values,
		},
	}

	// Build + inject the target cluster's kubeconfig only when a target is set.
	// A terraform component has no cluster target (TargetClusterID is uuid.Nil):
	// it gets no kubeconfig and must use an explicit state backend. Helm/manifest
	// always pass a real target.
	if in.TargetClusterID != uuid.Nil {
		targetConn, err := r.conns.ConnForTekton(ctx, in.OrgID, in.TargetClusterID)
		if err != nil {
			return Resolved{}, err
		}
		// When the runner is the target cluster, the job runs inside the cluster it
		// deploys to, so the injected kubeconfig must use the in-cluster API server
		// address rather than the registered (possibly host-only) endpoint.
		sameCluster := in.RunnerClusterID == in.TargetClusterID
		kubeconfig, err := k8s.Kubeconfig(ctx, targetConn, sameCluster)
		if err != nil {
			return Resolved{}, err
		}
		out.TargetMethod = targetConn.Method
		out.Files[helm.KubeconfigFile] = string(kubeconfig)
	}

	// Inject the application's variables (app-level overlaid by the component's
	// own) as the job's environment: non-secret values inline, sensitive ones via
	// the per-run Secret. Merged first so a credential-derived key (AWS_REGION
	// below) wins over a same-named variable; a final reconciliation keeps the two
	// maps disjoint.
	if r.vars != nil && in.ApplicationID != uuid.Nil {
		plain, secret, err := r.vars.ResolveEnv(ctx, in.OrgID, in.ApplicationID, in.ComponentID)
		if err != nil {
			return Resolved{}, err
		}
		if len(plain) > 0 {
			out.Env = plain
		}
		if len(secret) > 0 {
			out.SecretEnv = secret
		}
	}

	// Attach a private-chart credential, when one is set: resolve (decrypt) it and
	// inject the username/password as mounted files. The script reads them at
	// runtime, so the password never lands in the script string, the TaskRun
	// manifest, or env — only in the same owner-referenced, GC'd Secret as the
	// kubeconfig. Uninstall pulls no chart, so it needs no credential.
	if in.ChartCredentialID != uuid.Nil && in.PullsChart {
		if r.creds == nil {
			return Resolved{}, fmt.Errorf("deploy: a chart credential is referenced but the chart-credentials service is not configured")
		}
		cred, err := r.creds.Resolve(ctx, in.OrgID, in.ChartCredentialID)
		if err != nil {
			return Resolved{}, err
		}
		out.Files[helm.RegistryUsernameFile] = cred.Username
		out.Files[helm.RegistryPasswordFile] = cred.Password
		out.HasCredential = true
	}

	// Attach a GitHub App installation token, when one is set, for a private-Git
	// chart: mint it late (this attempt) so River retries always carry a fresh
	// token, and inject it as the mounted git-credentials file. The script wires
	// git's credential helper to it, so the token never lands in the script
	// string, the clone's argv, or the workspace .git/config. Uninstall pulls no
	// chart, so it needs no token.
	if in.GitHubInstallationID != uuid.Nil && in.PullsChart {
		if r.gitTokens == nil {
			return Resolved{}, fmt.Errorf("deploy: a github installation is referenced but the github-installations service is not configured")
		}
		token, err := r.gitTokens.InstallationToken(ctx, in.OrgID, in.GitHubInstallationID)
		if err != nil {
			return Resolved{}, err
		}
		out.Files[helm.GitCredentialsFile] = "https://x-access-token:" + token + "@github.com"
		out.HasGitToken = true
	}

	// Attach a cloud credential, when one is set (a terraform component): resolve
	// (decrypt) it, materialize the AWS env (pre-assuming any role via
	// STS), and write the secret keys into a sourceable env file as `export
	// K='V'` lines. The keys (access/secret/token) land ONLY in the mounted file
	// — never in out.Env, the script string, or the TaskRun manifest, exactly as
	// the chart password and git token do. Only the non-secret region is routed
	// to out.Env so the planner can put it on the pod env block. Unlike the
	// chart-credential and git-token blocks above, this is NOT gated on PullsChart:
	// a terraform uninstall (tofu destroy) still reads remote state and so still
	// needs backend auth, so the credential is honored for every action.
	if in.CloudCredentialID != uuid.Nil {
		if r.cloudCreds == nil {
			return Resolved{}, fmt.Errorf("deploy: a cloud credential is referenced but the cloud-credentials service is not configured")
		}
		resolved, err := r.cloudCreds.Resolve(ctx, in.OrgID, in.CloudCredentialID)
		if err != nil {
			return Resolved{}, err
		}
		secretEnv, region, err := cloudauth.AWSEnv(ctx, resolved)
		if err != nil {
			return Resolved{}, err
		}
		out.Files[tofu.AWSEnvFile] = renderEnvFile(secretEnv)
		if region != "" {
			if out.Env == nil {
				out.Env = map[string]string{}
			}
			out.Env["AWS_REGION"] = region
		}
		out.HasCloudAuth = true

		// First-party DynamoDB state locking: when the component names a lock
		// table, make sure it exists before the step runs — created with the
		// schema OpenTofu requires when missing — using the same materialized
		// credential the step gets (no second STS round trip). Idempotent and
		// cheap (one DescribeTable when the table exists), so it runs for both
		// the plan and apply units. A definitely-missing table that cannot be
		// created fails the run here, with a clearer error than a mid-step
		// lock failure.
		if in.DynamoDBLockTable != "" {
			if err := r.ensureLockTable(ctx, secretEnv, in.DynamoDBLockRegion, in.DynamoDBLockTable); err != nil {
				return Resolved{}, err
			}
		}
	}

	// Keep Env and SecretEnv disjoint: a credential-injected or non-secret key
	// (in Env) wins over a sensitive variable of the same name, so the step never
	// gets two env entries for one name.
	for k := range out.Env {
		delete(out.SecretEnv, k)
	}
	return out, nil
}

// renderEnvFile builds a sourceable /bin/sh file with one `export K='V'` line
// per entry, in sorted key order for stable output, single-quoting (and
// escaping embedded single quotes in) each value so the credential values are
// safe to source.
func renderEnvFile(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shQuote(env[k]))
	}
	return b.String()
}

// shQuote single-quotes a value for safe sourcing in a /bin/sh env file,
// escaping any embedded single quote as '\”. Mirrors lib/tofu's shQuote.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
