package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Organization is the top-level tenant. Everything else in the app belongs to
// an organization, and a user must have one selected to use the app. Users
// join organizations through the Membership join entity.
type Organization struct {
	ent.Schema
}

func (Organization) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("name").NotEmpty(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Organization) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("users", User.Type).
			Ref("organizations").
			Through("memberships", Membership.Type),
	}
}
