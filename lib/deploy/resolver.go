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

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
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

// Resolver holds the three run-time dependencies (connection / credential / git
// token) and is the single implementation of the run input resolution. Construct
// it with the same deps the applications service already holds.
type Resolver struct {
	conns     ConnResolver
	creds     CredentialResolver
	gitTokens GitTokenResolver
}

// NewResolver builds a Resolver over the connection, credential, and git-token
// resolvers. creds/gitTokens may be nil; a run that references one then fails
// with a clear "not configured" error.
func NewResolver(conns ConnResolver, creds CredentialResolver, gitTokens GitTokenResolver) *Resolver {
	return &Resolver{conns: conns, creds: creds, gitTokens: gitTokens}
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
	return out, nil
}
