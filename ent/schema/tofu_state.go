package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TofuLockInfo is OpenTofu's state lock description, exactly as the `http`
// backend sends it in a lock request (statemgr.LockInfo's JSON). It is
// stored whole and handed back to a client whose lock request conflicts, so
// OpenTofu can print who holds the lock.
type TofuLockInfo struct {
	ID        string `json:"ID"`
	Operation string `json:"Operation"`
	Info      string `json:"Info"`
	Who       string `json:"Who"`
	Version   string `json:"Version"`
	Created   string `json:"Created"`
	Path      string `json:"Path"`
}

// TofuState is the managed state of one OpenTofu component workspace — the
// state Spacefleet serves over OpenTofu's `http` backend for a component
// whose backend is "spacefleet" (see lib/tofustate). One row per
// (organization, application, component, workspace): the current version
// pointer with its serial and lineage, and the lock.
//
// component_id is a bare column with no edge on purpose: a workflow save
// deletes and recreates components, and a component removed from the
// workflow still owns state that tracks real infrastructure. The row goes
// with its application (ON DELETE CASCADE).
type TofuState struct {
	ent.Schema
}

func (TofuState) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names
		// match the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		// The authored component (no FK, see the type doc).
		field.UUID("component_id", uuid.UUID{}).Immutable(),
		// The component's workspace setting ("default" when unset).
		field.String("workspace").Default("default").Immutable(),
		// The newest version's number (0 = no state written yet), and that
		// version's serial and lineage, which the write guards compare against.
		field.Int("current_version").Default(0),
		field.Int64("serial").Default(0),
		field.String("lineage").Default(""),
		// The lock: the holder's lock id and full lock info, when it was taken,
		// and the component run (execution step) holding it. All nil when
		// unlocked.
		field.String("lock_id").Optional().Nillable(),
		field.JSON("lock_info", &TofuLockInfo{}).Optional(),
		field.Time("locked_at").Optional().Nillable(),
		field.UUID("locked_by_component_run_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TofuState) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
		edge.To("versions", TofuStateVersion.Type),
	}
}

func (TofuState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		index.Fields("organization_id", "application_id", "component_id", "workspace").Unique(),
	}
}
