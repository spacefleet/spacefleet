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
// application itself holds only the workflow-owner fields: a name and a runner
// cluster (a Tekton-enabled cluster) where the management jobs execute. All
// helm/chart-specific config and targeting (target cluster + namespace) live on
// the individual components — the application has no app-level target — and all
// run/sync lifecycle lives on the WorkflowRun / ComponentRun.
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
		// FK column bound to the runner_cluster edge below: the Tekton-enabled
		// cluster the management jobs run on. There is no app-level target cluster
		// or namespace — targeting lives on the individual components.
		field.UUID("runner_cluster_id", uuid.UUID{}),
		// Optional membership in a top-level ApplicationGroup (a folder). When
		// unset (uuid.Nil) the application sits at the org root. Bound to the group
		// edge below; ON DELETE SET NULL in the migration (deleting a group
		// ungroups its applications rather than deleting them).
		field.UUID("group_id", uuid.UUID{}).Optional(),
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
