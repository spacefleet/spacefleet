package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// CloudCredential is a named set of cloud-provider credentials registered to an
// organization (AWS, GCP, or Azure). It is the foundation other features build
// on — cluster registration, pulling private packages in workflows, etc. — so
// the credential itself only stores and seals the secret; consumers decrypt it
// on demand via the service's Resolve seam.
//
// The provider is fixed at creation. Non-secret identifiers (region, project,
// tenant/subscription id, …) live in config so they can be displayed; the
// secret material (access keys, service-account JSON, client secret) is
// envelope-encrypted into encrypted_credentials (see lib/secrets) and is never
// returned to the browser. An organization may register as many as it wants,
// including several of the same provider; names are unique within an org.
type CloudCredential struct {
	ent.Schema
}

func (CloudCredential) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// Which cloud the credential is for. Fixed at registration: changing it
		// would invalidate the stored secret shape.
		field.Enum("provider").
			Values("aws", "gcp", "azure").
			Immutable(),
		field.String("description").Optional(),
		// Non-secret, per-provider identifiers (region, role_arn, project,
		// tenant_id, client_id, subscription_id, …). Stored as a flat string map
		// so the shape can vary per provider without a migration.
		field.JSON("config", map[string]string{}).Optional(),
		// Envelope-encrypted credential blob (see lib/secrets). Nillable so a NULL
		// column doesn't surface as a zero-length non-nil slice.
		field.Bytes("encrypted_credentials").Optional().Nillable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (CloudCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (CloudCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Credential names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
