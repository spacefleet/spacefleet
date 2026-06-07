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
// for a terraform component running in byo backend mode that authenticates to
// the cloud. May be nil (no cloud-credentials service wired), in which case a
// run referencing a credential fails with a clear error.
type CloudCredentialResolver interface {
	Resolve(ctx context.Context, orgID, id uuid.UUID) (cloudcredentials.Resolved, error)
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
}

// NewResolver builds a Resolver over the connection, credential, git-token, and
// cloud-credential resolvers. creds/gitTokens/cloudCreds may be nil; a run that
// references one then fails with a clear "not configured" error.
func NewResolver(conns ConnResolver, creds CredentialResolver, gitTokens GitTokenResolver, cloudCreds CloudCredentialResolver) *Resolver {
	return &Resolver{conns: conns, creds: creds, gitTokens: gitTokens, cloudCreds: cloudCreds}
}

// RunInputs is the generic description of one helm run the resolver needs,
// decoupled from any ent row. The caller (applications or workflows) fills it
// from its own model.
type RunInputs struct {
	OrgID uuid.UUID
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
	// terraform byo-backend run to the cloud (AWS). uuid.Nil when none.
	CloudCredentialID uuid.UUID
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
	// Env is non-secret environment the step exports (e.g. AWS_REGION). The
	// planner threads it into the RunSpec.Env. Secret credential keys NEVER go
	// here — only into the mounted Files[tofu.AWSEnvFile].
	Env map[string]string
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
	targetConn, err := r.conns.ConnForTekton(ctx, in.OrgID, in.TargetClusterID)
	if err != nil {
		return Resolved{}, err
	}
	// When the runner is the target cluster, the Helm job runs inside the cluster
	// it deploys to, so the injected kubeconfig must use the in-cluster API server
	// address rather than the registered (possibly host-only) endpoint.
	sameCluster := in.RunnerClusterID == in.TargetClusterID
	kubeconfig, err := k8s.Kubeconfig(ctx, targetConn, sameCluster)
	if err != nil {
		return Resolved{}, err
	}

	out := Resolved{
		RunnerConn:   runnerConn,
		TargetMethod: targetConn.Method,
		Files: map[string]string{
			helm.KubeconfigFile: string(kubeconfig),
			helm.ValuesFile:     in.Values,
		},
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

	// Attach a cloud credential, when one is set (terraform byo backend mode):
	// resolve (decrypt) it, materialize the AWS env (pre-assuming any role via
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
