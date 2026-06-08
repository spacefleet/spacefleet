package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Variable is a named key/value passed to a workflow's component jobs as an
// environment variable. It lives at one of two levels, distinguished by
// component_id:
//   - app-level (component_id NULL): passed to every component job of the app.
//   - component-level (component_id set): passed to that one component's job, and
//     overrides an app-level variable of the same name.
//
// A variable is either non-secret — value stored and returned in plaintext — or
// sensitive — value envelope-encrypted into encrypted_value (see lib/secrets) and
// never returned to any caller (write-only; it can only be replaced), mirroring
// how ChartCredential treats its password.
//
// component_id is a plain column with NO edge/foreign key on purpose:
// workflows.ReplaceWorkflow deletes and recreates every component (with stable
// client ids) on each workflow save, so a cascading FK would wipe component
// variables on every save. Orphans (a component removed from the workflow) are
// reconciled inside that same transaction instead. Like every resource it carries
// organization_id so every service query is org-scoped (the tenancy boundary).
type Variable struct {
	ent.Schema
}

func (Variable) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the names match the
		// hand-written migration. Both immutable: a variable belongs to one org and
		// one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		// The component this variable scopes to, or nil for an app-level variable.
		// Immutable and intentionally NOT an edge (no FK) — see the type comment.
		field.UUID("component_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		// The environment variable name (validated as a shell-safe identifier in the
		// service).
		field.String("name").NotEmpty(),
		// True when the value is sensitive: sealed at rest and never returned.
		field.Bool("sensitive").Default(false),
		// The plaintext value for a non-secret variable. Empty for a sensitive one
		// (whose value lives in encrypted_value).
		field.String("value").Optional(),
		// Envelope-encrypted value blob for a sensitive variable (see lib/secrets).
		// Nillable so a NULL column doesn't surface as a zero-length non-nil slice.
		field.Bytes("encrypted_value").Optional().Nillable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Variable) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this variable belongs to. ON DELETE CASCADE in the
		// migration: a variable disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (Variable) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's variables (both levels) and resolving them at run
		// time.
		index.Fields("application_id", "component_id"),
	}
}
