package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// WorkflowRun is one logical run of an application's deploy workflow — a single
// deploy / uninstall / preview of the whole DAG, recorded so the application
// keeps a CI-like history of its runs. Each node's execution within the run is a
// ComponentRun child. graph holds a JSON snapshot of the nodes + edges + config
// as they were when the run began, so an in-flight run is immune to later edits
// of the workflow (and a past run stays auditable). The service marshals /
// unmarshals it, so ent stays decoupled from the snapshot's Go shape.
//
// status settles partial when only continue-on-failure nodes failed, failed on a
// hard failure, succeeded otherwise. Like every resource it carries
// organization_id so every service query is org-scoped (the tenancy boundary),
// not via the application join alone.
type WorkflowRun struct {
	ent.Schema
}

func (WorkflowRun) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a run belongs to one org and
		// one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		// What the run does across the whole workflow.
		field.Enum("action").
			Values("deploy", "uninstall", "preview"),
		// Run lifecycle: pending → running → succeeded / failed / partial.
		field.Enum("status").
			Values("pending", "running", "succeeded", "failed", "partial").
			Default("pending"),
		// Human-readable detail: a progress line or the terminal summary.
		field.String("message").Optional(),
		// River job id of the workflow job driving this run, for correlation.
		field.String("job_id").Optional(),
		// JSON snapshot of the workflow (nodes + edges + config) as it was when the
		// run began. Text so ent stays decoupled from the Go snapshot type; the
		// service marshals/unmarshals.
		field.Text("graph").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		// When the run started executing (nil while pending).
		field.Time("started_at").Optional().Nillable(),
		// When the run settled (nil until terminal).
		field.Time("finished_at").Optional().Nillable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (WorkflowRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this run belongs to. ON DELETE CASCADE in the migration:
		// a run disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (WorkflowRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's runs newest-first.
		index.Fields("application_id", "created_at"),
		// Correlating the worker's job-id transitions back to the in-flight run.
		index.Fields("organization_id", "job_id"),
	}
}
