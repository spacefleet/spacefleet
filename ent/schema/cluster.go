package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Cluster is a Kubernetes cluster registered to an organization. It records how
// to reach the cluster (connection_method + non-secret config) and the result
// of the most recent connectivity probe. Any credential needed to connect
// (kubeconfig, bearer token, cloud credentials) is envelope-encrypted into
// encrypted_credentials and is never returned to the browser.
type Cluster struct {
	ent.Schema
}

func (Cluster) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// How Spacefleet connects:
		//   in_cluster — Spacefleet runs in this cluster; use its service account
		//   kubeconfig — a supplied kubeconfig (with embedded static credentials)
		//   token      — explicit API URL + CA + ServiceAccount bearer token
		//   eks/gke/aks — cloud-native auth (credentials minted at connect time)
		field.Enum("connection_method").
			Values("in_cluster", "kubeconfig", "token", "eks", "gke", "aks"),
		// API server URL for display. May be empty (in-cluster) or discovered
		// from the cloud provider (eks/gke/aks).
		field.String("endpoint").Optional(),
		// Non-secret, per-method parameters (region, project, location, cluster
		// name, resource group, subscription, insecure_skip_tls, …). Stored as a
		// flat string map so the shape can vary per method without a migration.
		field.JSON("config", map[string]string{}).Optional(),
		// Envelope-encrypted credential blob (see lib/secrets). Nil for
		// in_cluster, which needs no stored credential.
		field.Bytes("encrypted_credentials").Optional().Nillable().Sensitive(),
		// Result of the most recent connectivity probe.
		field.Enum("status").
			Values("pending", "connected", "error").
			Default("pending"),
		field.String("status_message").Optional(),
		field.String("k8s_version").Optional(),
		field.Time("last_checked_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Cluster) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (Cluster) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Cluster names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
