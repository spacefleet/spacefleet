package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Application is a deployable workload registered to an organization. It owns a
// deploy workflow — a DAG of typed Components (see ent/schema/component.go) — and
// a run of that workflow is a WorkflowRun with one ComponentRun per node. The
// application itself holds only the workflow-owner fields: a name, the app-level
// default target cluster + namespace its components deploy into (each component
// may override), and a runner cluster (a Tekton-enabled cluster) where the
// management jobs execute. All helm/chart-specific config lives on the
// components, and all run/sync lifecycle lives on the WorkflowRun / ComponentRun.
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
		// the cluster (the import flow) rather than created from scratch. The flag
		// lets the UI surface that the workflow is a best-effort reconstruction of
		// a live release. Defaults false (created, not imported).
		field.Bool("imported").Default(false),
		// The app-level default namespace in the target cluster components deploy
		// into; a component may override it.
		field.String("target_namespace").NotEmpty(),
		// FK columns bound to the cluster edges below. target_cluster is the
		// app-level default a component deploys into (overridable per component);
		// runner_cluster is the Tekton-enabled cluster the management jobs run on.
		field.UUID("target_cluster_id", uuid.UUID{}),
		field.UUID("runner_cluster_id", uuid.UUID{}),
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
		// The app-level default cluster components deploy into. RESTRICT in the
		// migration: an app blocks deletion of a cluster it targets.
		edge.To("target_cluster", Cluster.Type).
			Field("target_cluster_id").
			Unique().
			Required(),
		// The Tekton-enabled cluster the management jobs run on.
		edge.To("runner_cluster", Cluster.Type).
			Field("runner_cluster_id").
			Unique().
			Required(),
	}
}

func (Application) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Application names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
