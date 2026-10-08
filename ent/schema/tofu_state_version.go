package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TofuStateVersion is one accepted write of a managed OpenTofu state
// (TofuState): append-only, numbered per state. The state JSON is stored
// gzipped and then sealed with the deployment's secret key — state carries
// every attribute value, secrets included. The run ids say which step wrote
// it; they are bare columns so the history outlives the run.
type TofuStateVersion struct {
	ent.Schema
}

func (TofuStateVersion) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("state_id", uuid.UUID{}).Immutable(),
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		// Monotonic per state, starting at 1.
		field.Int("version").Immutable(),
		// The state file's own serial and lineage.
		field.Int64("serial").Immutable(),
		field.String("lineage").Immutable(),
		// gzip, then secrets.Sealer.Seal. Never returned by an API mapper.
		field.Bytes("sealed").Sensitive().Immutable(),
		// The uncompressed state size, and its MD5 (hex).
		field.Int64("size_bytes").Immutable(),
		field.String("md5").Immutable(),
		field.UUID("workflow_run_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		field.UUID("component_run_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		// Who started the run that wrote it (the run's started_by).
		field.String("created_by").Default("").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (TofuStateVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("state", TofuState.Type).
			Ref("versions").
			Field("state_id").
			Unique().
			Required().
			Immutable(),
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (TofuStateVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		index.Fields("state_id", "version").Unique(),
	}
}
