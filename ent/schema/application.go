package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Application is a deployable workload registered to an organization. The first
// (and for now only) type is a Helm release: it records the chart source and
// config (non-secret), a target cluster + namespace to deploy into, and a runner
// cluster (a Tekton-enabled cluster) where the management job — `helm upgrade
// --install` against the target via an injected kubeconfig — executes. status
// tracks the rollout lifecycle; last_run_name correlates the TaskRun on the
// runner so its progress can be streamed.
type Application struct {
	ent.Schema
}

func (Application) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// True when the application was adopted from a release already running on
		// the cluster (the import flow) rather than created and deployed by
		// Spacefleet. An imported app starts deployed without a rollout; the flag
		// lets the UI prompt a refresh to confirm the configured chart source
		// reproduces the live release. Defaults false (created, not imported).
		field.Bool("imported").Default(false),
		// Application type. Only "helm" today; the enum lets the create dropdown
		// grow later without a migration churn for the common case.
		field.Enum("type").
			Values("helm").
			Default("helm"),
		// Where the chart comes from:
		//   http_repo — a classic HTTP Helm repository (repo_url + chart + version)
		//   oci       — an OCI registry reference (repo_url + version)
		//   git       — a chart in a Git repo (repo_url + git_ref + git_path)
		field.Enum("chart_source").
			Values("http_repo", "oci", "git"),
		// Non-secret, source-specific parameters (repo_url, chart, version,
		// git_ref, git_path). Stored as a flat string map so the shape can vary
		// per source without a migration, exactly as clusters stores per-method
		// config.
		field.JSON("config", map[string]string{}).Optional(),
		// Raw values.yaml override, injected into the rollout as a file.
		field.String("values").Optional(),
		// Optional values-from-git sources, orthogonal to the chart source: an
		// ordered list of git repos to pull values files from, each a flat string
		// map (repo_url, git_ref, path) — same stringly-typed shape as config, so
		// the schema stays free of a domain type. At rollout each is cloned and
		// layered with -f in order (earlier first), all beneath the inline values
		// above, which wins. Empty for an app whose values are inline-only.
		field.JSON("values_sources", []map[string]string{}).Optional(),
		// The Helm release name; defaults to name when empty.
		field.String("release_name").Optional(),
		// The namespace in the target cluster the release is installed into.
		field.String("target_namespace").NotEmpty(),
		// FK columns bound to the cluster edges below.
		field.UUID("target_cluster_id", uuid.UUID{}),
		field.UUID("runner_cluster_id", uuid.UUID{}),
		// Optional chart credential for pulling a private chart. Bound to the
		// chart_credential edge below; nil for public charts. Its type must match
		// chart_source (basic_auth → http_repo, oci → oci), enforced in the service.
		field.UUID("chart_credential_id", uuid.UUID{}).Optional(),
		// Optional GitHub App installation for pulling a private Git chart. Bound
		// to the github_installation edge below; nil for public repos. Only valid
		// when chart_source is git, enforced in the service.
		field.UUID("github_installation_id", uuid.UUID{}).Optional(),
		// Rollout lifecycle: pending (created, never rolled out) → deploying →
		// deployed/failed, plus uninstalling → uninstalled.
		field.Enum("status").
			Values("pending", "deploying", "deployed", "failed", "uninstalling", "uninstalled").
			Default("pending"),
		field.String("status_message").Optional(),
		// Id of the in-flight rollout job, for correlation.
		field.String("job_id").Optional(),
		// The TaskRun name on the runner cluster for the most recent rollout, used
		// to stream live status and logs.
		field.String("last_run_name").Optional(),
		// Sync (preview/diff) state, kept strictly disjoint from the rollout fields
		// above (status/status_message/job_id/last_run_name) so a refresh — which
		// runs `helm diff` as a TaskRun and changes nothing — never clobbers an
		// in-flight rollout, and vice versa. A refresh re-resolves the desired state
		// (latest git SHA / chart version / values) and diffs it against the live
		// cluster.
		//   unknown      — never refreshed
		//   refreshing   — a preview job is in flight
		//   synced       — desired state matches the live cluster (no changes)
		//   out_of_sync  — deploying would change the live cluster (see last_diff)
		//   error        — the diff run failed (see sync_message)
		field.Enum("sync_status").
			Values("unknown", "refreshing", "synced", "out_of_sync", "error").
			Default("unknown"),
		field.String("sync_message").Optional(),
		// The captured `helm diff` output from the most recent refresh, shown in the
		// deploy-confirmation UI. Text (unbounded), like values; truncated by the
		// service before write so a pathological all-additions diff can't bloat the row.
		field.Text("last_diff").Optional(),
		// The git commit SHA the chart resolved to at the last refresh (empty for a
		// non-git chart), and the newline-joined "<repo>@<sha>" lines each values
		// source resolved to — the desired revisions a deploy would pull right now.
		field.String("desired_chart_revision").Optional(),
		field.String("desired_values_revision").Optional(),
		// When the last refresh completed.
		field.Time("last_refreshed_at").Optional(),
		// Id of the in-flight preview (refresh) job, for correlation.
		field.String("sync_job_id").Optional(),
		// The TaskRun name on the runner cluster for the most recent preview run.
		// Separate from last_run_name so a preview retry re-attaches to the preview
		// run, never the last rollout run.
		field.String("sync_run_name").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Application) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The cluster the release is deployed into. RESTRICT in the migration: an
		// app blocks deletion of a cluster it targets.
		edge.To("target_cluster", Cluster.Type).
			Field("target_cluster_id").
			Unique().
			Required(),
		// The Tekton-enabled cluster the management job runs on.
		edge.To("runner_cluster", Cluster.Type).
			Field("runner_cluster_id").
			Unique().
			Required(),
		// Optional credential for pulling a private chart. RESTRICT in the
		// migration: a credential in use can't be deleted out from under an app.
		edge.To("chart_credential", ChartCredential.Type).
			Field("chart_credential_id").
			Unique(),
		// Optional GitHub App installation for pulling a private Git chart.
		// RESTRICT in the migration: an installation in use can't be deleted out
		// from under an app.
		edge.To("github_installation", GitHubInstallation.Type).
			Field("github_installation_id").
			Unique(),
	}
}

func (Application) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Application names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
