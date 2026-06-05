package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Deployment is one rollout of an application — a single deploy/upgrade/uninstall
// attempt, recorded so the application keeps a CI-like history of its runs. The
// application row tracks only the *current* rollout (its status + last_run_name);
// a Deployment is the durable per-run record: which action ran, whether it
// succeeded, when, and the captured Helm output. Because the TaskRun pod on the
// runner is garbage-collected after the run, the logs are persisted here at
// terminal so a past run stays viewable.
//
// It is a child of an Application and, like every resource, org-scoped — it
// carries organization_id so the service can scope every query by org id (the
// security boundary), not by relying on the application join alone.
type Deployment struct {
	ent.Schema
}

func (Deployment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a run belongs to one org and
		// one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		// The rollout action this run performed. deploy and upgrade both run
		// `helm upgrade --install`; uninstall runs `helm uninstall`.
		field.Enum("action").
			Values("deploy", "upgrade", "uninstall"),
		// Run lifecycle: a run is created running, then settles succeeded/failed.
		field.Enum("status").
			Values("running", "succeeded", "failed").
			Default("running"),
		// Human-readable detail: the last rollout-progress line, or the error.
		field.String("message").Optional(),
		// River job id of the rollout that drives this run, used to correlate the
		// worker's MarkRollout transitions back to this row.
		field.String("job_id").Optional(),
		// The TaskRun name on the runner cluster for this run.
		field.String("run_name").Optional(),
		// The captured Helm output, persisted when the run reaches a terminal phase
		// (the runner pod is then garbage-collected, so this is the durable copy).
		field.Text("logs").Optional(),
		// The git commit SHAs this rollout actually resolved, parsed from the run's
		// logs at terminal. Because git-sourced charts/values are pulled on deploy
		// from a mutable ref (a branch can move between runs), recording the resolved
		// SHA makes a run auditable and reproducible. Empty when that source wasn't a
		// git clone (an http_repo/oci chart, or no values-from-git).
		field.String("chart_revision").Optional(),
		field.String("values_revision").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		// When the run settled (nil while still running).
		field.Time("finished_at").Optional().Nillable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Deployment) Edges() []ent.Edge {
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

func (Deployment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's runs newest-first.
		index.Fields("application_id", "created_at"),
		// Correlating the worker's job-id transitions back to the in-flight run.
		index.Fields("organization_id", "job_id"),
	}
}
