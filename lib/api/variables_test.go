package api

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
)

// TestToAPIVariableWriteOnly confirms the API mapping never exposes a sensitive
// variable's value (write-only), while a non-secret one returns its plaintext.
func TestToAPIVariableWriteOnly(t *testing.T) {
	now := time.Now()

	sensitive := &ent.Variable{
		ID: uuid.New(), Name: "API_KEY", Sensitive: true, Value: "",
		CreatedAt: now, UpdatedAt: now,
	}
	out := toAPIVariable(sensitive)
	if out.Value != nil {
		t.Errorf("sensitive variable exposed a value (%q); it must be write-only", *out.Value)
	}
	if !out.Sensitive {
		t.Error("Sensitive = false, want true")
	}

	plain := &ent.Variable{
		ID: uuid.New(), Name: "LOG_LEVEL", Sensitive: false, Value: "debug",
		CreatedAt: now, UpdatedAt: now,
	}
	got := toAPIVariable(plain)
	if got.Value == nil || *got.Value != "debug" {
		t.Errorf("non-secret value = %v, want debug", got.Value)
	}
}
