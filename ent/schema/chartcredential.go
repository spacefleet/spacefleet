package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ChartCredential is a named credential set, registered to an organization, for
// pulling private Helm charts. It is a basic-auth username/password pair that
// works for both an HTTP Helm repository (helm repo add --username/--password)
// and an OCI registry (helm registry login) — the chart pull picks the mechanism
// from the application's chart_source, so the credential itself needs no type.
// The password is envelope-encrypted into encrypted_password (see lib/secrets)
// and is never returned to the browser; the username is non-secret display
// detail, like a cluster endpoint.
//
// Surfaced in the UI as "Private Charts".
type ChartCredential struct {
	ent.Schema
}

func (ChartCredential) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// Non-secret registry/repo username.
		field.String("username").Optional(),
		// Envelope-encrypted password blob (see lib/secrets). Nillable so a NULL
		// column doesn't surface as a zero-length non-nil slice.
		field.Bytes("encrypted_password").Optional().Nillable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ChartCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (ChartCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Credential names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
