package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// NotificationChannel is a destination an organization sends run
// notifications to — an email address, a Slack incoming webhook, or a
// generic webhook — with the events it subscribes to and, optionally, the
// one application it is limited to. A webhook URL is a secret (whoever
// holds it can post as the integration), so it is envelope-encrypted into
// encrypted_target (see lib/secrets); address carries the display form only
// — the email address itself, or a webhook's host. See lib/notifications.
type NotificationChannel struct {
	ent.Schema
}

func (NotificationChannel) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// How the notification is delivered. Fixed at creation.
		field.Enum("kind").Values("email", "slack", "webhook").Immutable(),
		// The display form of the destination: the email address, or a
		// webhook's host. Never the secret URL.
		field.String("address"),
		// The sealed webhook URL (slack, webhook); nil for email.
		field.Bytes("encrypted_target").Optional().Nillable().Sensitive(),
		// The event kinds this channel receives (notifications.Event* values).
		field.JSON("events", []string{}).Optional(),
		// Optional: limit the channel to one application (uuid.Nil = every
		// application of the organization). ON DELETE CASCADE in the migration.
		field.UUID("application_id", uuid.UUID{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (NotificationChannel) Edges() []ent.Edge {
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

func (NotificationChannel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		index.Fields("organization_id", "name").Unique(),
	}
}
