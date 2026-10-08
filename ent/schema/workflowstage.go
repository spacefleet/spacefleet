package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// WorkflowStage is one stage of an application's deploy workflow: a named,
// ordered bucket of components. Stages run in ordinal order and the components
// within a stage run in parallel, so a stage starts once every component of the
// previous stage has finished. The scheduler never sees a stage — at run-snapshot
// time the service desugars stage order into component-level depends_on (every
// component of a stage waits on every component of the previous non-empty stage).
//
// Like every resource it carries organization_id so every service query is
// org-scoped (the tenancy boundary), not via the application join alone.
//
// ON DELETE: the hand-written SQL migration in db/migrations is the source of
// truth for foreign-key delete behavior, not these edges. The application edge
// is ON DELETE CASCADE there (a stage disappears with its application), and so
// is components.stage_id (a component disappears with its stage). ent
// auto-migrate is never run here, so the edge annotations exist only to generate
// the Go client (column names, types, loaders), not the DDL.
type WorkflowStage struct {
	ent.Schema
}

func (WorkflowStage) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a stage belongs to one org
		// and one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// The stage's position in run order, 0-based. The service rewrites every
		// ordinal on each workflow replace, so they are dense.
		field.Int("ordinal").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (WorkflowStage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this stage belongs to. ON DELETE CASCADE in the
		// migration: a stage disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
		// The components that run in this stage — the inverse of Component's stage
		// edge, so a workflow loads with its components eager-loaded.
		edge.From("components", Component.Type).
			Ref("stage"),
	}
}

func (WorkflowStage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's stages in order.
		index.Fields("application_id", "ordinal"),
	}
}
