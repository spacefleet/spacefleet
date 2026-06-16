package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/workflows"
)

// TestToAPIComponentOutputKeys proves the mapper keys the response by component
// id string, carries the sensitive flag, surfaces a type hint only when present,
// and (by construction of the schema) never exposes a value.
func TestToAPIComponentOutputKeys(t *testing.T) {
	id := uuid.New()
	typ := `"string"`
	out := toAPIComponentOutputKeys(map[uuid.UUID][]workflows.OutputKey{
		id: {
			{Key: "vpc_id", Type: typ, Sensitive: false},
			{Key: "db_password", Sensitive: true}, // no type hint
		},
	})

	keys, ok := out[id.String()]
	if !ok {
		t.Fatalf("missing component id %s in %v", id, out)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(keys))
	}
	if keys[0].Key != "vpc_id" || keys[0].Sensitive {
		t.Errorf("keys[0] = %+v, want non-sensitive vpc_id", keys[0])
	}
	if keys[0].Type == nil || *keys[0].Type != typ {
		t.Errorf("keys[0].Type = %v, want %q", keys[0].Type, typ)
	}
	if keys[1].Key != "db_password" || !keys[1].Sensitive {
		t.Errorf("keys[1] = %+v, want sensitive db_password", keys[1])
	}
	if keys[1].Type != nil {
		t.Errorf("keys[1].Type = %v, want nil (no hint)", keys[1].Type)
	}
}
