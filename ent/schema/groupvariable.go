package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// GroupVariable is a named key/value defined on an ApplicationGroup and passed to
// the component jobs of every application in that group as an environment
// variable. It is the lowest-priority variable level: an application-level
// Variable of the same name overrides it, and a component-level one overrides
// that (group < app < component). See ent/schema/variable.go for the app- and
// component-level Variable; the two tables share the same shape but differ in
// scope (group_id here vs application_id/component_id there).
//
// A variable is either non-secret — value stored and returned in plaintext — or
// sensitive — value envelope-encrypted into encrypted_value (see lib/secrets) and
// never returned to any caller (write-only; it can only be replaced). Like every
// resource it carries organization_id so every service query is org-scoped (the
// tenancy boundary).
type GroupVariable struct {
	ent.Schema
}

func (GroupVariable) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the names match the
		// hand-written migration. Both immutable: a variable belongs to one org and
		// one group for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("group_id", uuid.UUID{}).Immutable(),
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

func (GroupVariable) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application group this variable belongs to. ON DELETE CASCADE in the
		// migration: a variable disappears with its group (and a group's deletion
		// only ungroups its applications, so their app/component variables survive).
		edge.To("group", ApplicationGroup.Type).
			Field("group_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (GroupVariable) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Variable names are unique within a group; this index also serves listing
		// and resolving a group's variables.
		index.Fields("group_id", "name").Unique(),
	}
}
