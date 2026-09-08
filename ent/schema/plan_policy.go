package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PlanPolicy is a policy-as-code rule an organization applies to OpenTofu
// plans: a Rego module (package spacefleet, a `deny` rule) evaluated
// against every plan of the applications it covers, before the apply.
// enforcement decides what a violation does — block fails the plan step
// (the apply never runs), warn records it for the approver. Optionally
// limited to one application. See lib/policy for the engine and
// lib/policies for the service.
type PlanPolicy struct {
	ent.Schema
}

// Annotations pins the table name: "Policy" is an ent-reserved identifier,
// so the type is PlanPolicy while the table (and the API) say "policies".
func (PlanPolicy) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "policies"}}
}

func (PlanPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		field.String("description").Optional(),
		// The Rego source, validated at write time (policy.Compile).
		field.Text("rego"),
		field.Enum("enforcement").Values("block", "warn").Default("block"),
		field.Bool("enabled").Default(true),
		// Optional: limit the policy to one application (uuid.Nil = every
		// application of the organization). ON DELETE CASCADE in the migration.
		field.UUID("application_id", uuid.UUID{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (PlanPolicy) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		edge.To("application", Application.Type).
			Field("application_id").
			Unique(),
	}
}

func (PlanPolicy) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		index.Fields("organization_id", "name").Unique(),
	}
}
