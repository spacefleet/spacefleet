package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Component is one node of an application's deploy workflow — a typed step in the
// DAG the application owns. The first types are a Helm release ("helm") and a
// manifest apply ("manifest"); the enum lets the set grow (Terraform, …) without
// a migration churn. Type-specific, non-secret parameters live in config, a flat
// string map validated per type in the service (helm: chart_source/repo_url/
// chart/version/git_ref/git_path/values/values_sources/release_name; manifest:
// repo_url/git_ref/path), mirroring how Application stored its chart config.
//
// depends_on lists the sibling components this one waits on; concurrency is "no
// unmet deps". continue_on_failure decides whether a failed node skips its
// dependents (false) or not (true → the run goes "partial"). target_cluster_id /
// target_namespace are optional per-component overrides of the application's
// app-level defaults; chart_credential_id / github_installation_id mirror the
// application's optional private-pull credentials. position is the canvas {x,y}
// for the builder UI.
//
// Like every resource it carries organization_id so every service query is
// org-scoped (the tenancy boundary), not via the application join alone.
type Component struct {
	ent.Schema
}

func (Component) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a component belongs to one org
		// and one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// The component type. Only helm + manifest today; the enum lets the set grow
		// without a migration for the common case.
		field.Enum("type").
			Values("helm", "manifest").
			Default("helm"),
		// Non-secret, type-specific parameters. Stored as a flat string map so the
		// shape can vary per type without a migration, exactly as Application stored
		// its chart config. Validated per type in the service.
		field.JSON("config", map[string]string{}).Optional(),
		// Ids of sibling components this node waits on. The graph is a DAG; the
		// service validates it acyclic at write time.
		field.JSON("depends_on", []uuid.UUID{}).Optional(),
		// When true, a failure of this node does not skip its dependents or fail the
		// run (the run settles "partial" instead).
		field.Bool("continue_on_failure").Default(false),
		// Optional per-component override of the application's default target cluster.
		// Bound to the target_cluster edge below; RESTRICT in the migration.
		field.UUID("target_cluster_id", uuid.UUID{}).Optional(),
		// Optional per-component override of the application's default target
		// namespace.
		field.String("target_namespace").Optional(),
		// Optional credential for pulling a private chart, mirroring Application.
		// Bound to the chart_credential edge below; RESTRICT in the migration.
		field.UUID("chart_credential_id", uuid.UUID{}).Optional(),
		// Optional GitHub App installation for pulling a private git source,
		// mirroring Application. Bound to the github_installation edge below;
		// RESTRICT in the migration.
		field.UUID("github_installation_id", uuid.UUID{}).Optional(),
		// Canvas coordinates {x, y} for the workflow builder UI.
		field.JSON("position", map[string]float64{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Component) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this component belongs to. ON DELETE CASCADE in the
		// migration: a component disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
		// Optional per-component target cluster override. RESTRICT in the migration:
		// a cluster a component targets can't be deleted out from under it.
		edge.To("target_cluster", Cluster.Type).
			Field("target_cluster_id").
			Unique(),
		// Optional credential for pulling a private chart. RESTRICT in the migration.
		edge.To("chart_credential", ChartCredential.Type).
			Field("chart_credential_id").
			Unique(),
		// Optional GitHub App installation for pulling a private git source.
		// RESTRICT in the migration.
		edge.To("github_installation", GitHubInstallation.Type).
			Field("github_installation_id").
			Unique(),
	}
}

func (Component) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's components.
		index.Fields("application_id"),
	}
}
