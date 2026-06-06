package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ComponentRun is the execution of one workflow node within a WorkflowRun — the
// durable per-step record (which component ran, whether it passed, when, the
// captured output). It is the workflow analogue of the old per-app Deployment row:
// a WorkflowRun is the run, its ComponentRuns are the steps. logs is persisted at
// terminal because the runner's TaskRun pod is garbage-collected after the run,
// so this is the durable copy.
//
// component_id is a *bare* nullable column with no edge: the source Component may
// be edited or deleted after this run, and a past run's record must survive that —
// so it is not FK-coupled. name/type are denormalized snapshots for the same
// reason (the run reads as it ran, independent of the live component). Like every
// resource it carries organization_id so every service query is org-scoped (the
// tenancy boundary), not via the workflow-run join alone.
type ComponentRun struct {
	ent.Schema
}

func (ComponentRun) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a step belongs to one org and
		// one workflow run for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("workflow_run_id", uuid.UUID{}).Immutable(),
		// The source component, denormalized: a bare nullable column with no edge,
		// so a past run survives the component being edited or deleted.
		field.UUID("component_id", uuid.UUID{}).Optional(),
		// Snapshot of the component's name and type as it ran. type is a plain string
		// (not the Component enum) to stay decoupled from the live component schema.
		field.String("name").Optional(),
		field.String("type").Optional(),
		// Step lifecycle: pending → running → succeeded / failed / skipped.
		field.Enum("status").
			Values("pending", "running", "succeeded", "failed", "skipped").
			Default("pending"),
		// Human-readable detail: the last progress line, or the error.
		field.String("message").Optional(),
		// The TaskRun name on the runner cluster for this step.
		field.String("run_name").Optional(),
		// The captured output, persisted when the step reaches a terminal phase (the
		// runner pod is then garbage-collected, so this is the durable copy).
		field.Text("logs").Optional(),
		// The git commit SHAs this step actually resolved (chart / values), for an
		// auditable, reproducible run. Empty when the source wasn't a git clone.
		field.String("chart_revision").Optional(),
		field.String("values_revision").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		// When the step started executing (nil while pending).
		field.Time("started_at").Optional().Nillable(),
		// When the step settled (nil until terminal).
		field.Time("finished_at").Optional().Nillable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ComponentRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The workflow run this step belongs to. ON DELETE CASCADE in the migration:
		// a step disappears with its run.
		edge.To("workflow_run", WorkflowRun.Type).
			Field("workflow_run_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (ComponentRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing a workflow run's steps.
		index.Fields("workflow_run_id"),
	}
}
