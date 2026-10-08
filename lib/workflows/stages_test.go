package workflows

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestStageDependencies proves stage order desugars into the component-level
// edges the scheduler runs: the first non-empty stage waits on nothing,
// components of one stage never wait on each other, every component of a stage
// waits on every component of the previous non-empty stage (an empty stage is
// skipped, not a break in the chain), and no two components share a slice.
func TestStageDependencies(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	stages := []StageInput{
		{ID: uuid.New(), Components: nil},
		{ID: uuid.New(), Components: []ComponentInput{{ID: a}, {ID: b}}},
		{ID: uuid.New(), Components: nil},
		{ID: uuid.New(), Components: []ComponentInput{{ID: c}}},
		{ID: uuid.New(), Components: []ComponentInput{{ID: d}}},
	}
	deps := stageDependencies(stages)

	if len(deps[a]) != 0 || len(deps[b]) != 0 {
		t.Errorf("first stage should wait on nothing: a=%v b=%v", deps[a], deps[b])
	}
	if len(deps[c]) != 2 || deps[c][0] != a || deps[c][1] != b {
		t.Errorf("c should wait on all of the previous non-empty stage, got %v", deps[c])
	}
	if len(deps[d]) != 1 || deps[d][0] != c {
		t.Errorf("d should wait on the previous stage only, got %v", deps[d])
	}
	if len(deps) != 4 {
		t.Errorf("every component should have an entry, got %d", len(deps))
	}

	// Mutating one component's edges must not leak into a sibling's.
	sib := uuid.New()
	shared := stageDependencies([]StageInput{
		{ID: uuid.New(), Components: []ComponentInput{{ID: a}}},
		{ID: uuid.New(), Components: []ComponentInput{{ID: b}, {ID: sib}}},
	})
	shared[b][0] = uuid.Nil
	if shared[sib][0] != a {
		t.Error("components of one stage share a dependency slice")
	}
}

func TestValidateWorkflow_Stages(t *testing.T) {
	stage := func(name string, comps ...ComponentInput) StageInput {
		return StageInput{ID: uuid.New(), Name: name, Components: comps}
	}

	t.Run("empty workflow and empty stages pass", func(t *testing.T) {
		if err := validateWorkflow(nil); err != nil {
			t.Fatalf("empty workflow: %v", err)
		}
		if err := validateWorkflow([]StageInput{stage("build"), stage("deploy", helmNode(uuid.New()))}); err != nil {
			t.Fatalf("empty stage: %v", err)
		}
	})

	t.Run("duplicate stage names are allowed", func(t *testing.T) {
		if err := validateWorkflow([]StageInput{stage("deploy"), stage("deploy")}); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("missing stage id", func(t *testing.T) {
		st := stage("deploy")
		st.ID = uuid.Nil
		if err := validateWorkflow([]StageInput{st}); !errors.Is(err, ErrMissingID) {
			t.Fatalf("expected ErrMissingID, got %v", err)
		}
	})

	t.Run("duplicate stage id", func(t *testing.T) {
		st := stage("deploy")
		if err := validateWorkflow([]StageInput{st, st}); !errors.Is(err, ErrDuplicateID) {
			t.Fatalf("expected ErrDuplicateID, got %v", err)
		}
	})

	t.Run("stage id reused as a component id", func(t *testing.T) {
		st := stage("deploy")
		st.Components = []ComponentInput{helmNode(st.ID)}
		if err := validateWorkflow([]StageInput{st}); !errors.Is(err, ErrDuplicateID) {
			t.Fatalf("expected ErrDuplicateID, got %v", err)
		}
	})

	t.Run("component in two stages", func(t *testing.T) {
		n := helmNode(uuid.New())
		if err := validateWorkflow([]StageInput{stage("a", n), stage("b", n)}); !errors.Is(err, ErrDuplicateID) {
			t.Fatalf("expected ErrDuplicateID, got %v", err)
		}
	})

	t.Run("blank stage name", func(t *testing.T) {
		for _, name := range []string{"", "   "} {
			if err := validateWorkflow([]StageInput{stage(name)}); !errors.Is(err, ErrInvalidStage) {
				t.Errorf("name %q: expected ErrInvalidStage, got %v", name, err)
			}
		}
	})

	t.Run("stage name length", func(t *testing.T) {
		// Counted in characters, not bytes.
		if err := validateWorkflow([]StageInput{stage(strings.Repeat("é", maxStageNameLen))}); err != nil {
			t.Fatalf("a %d-character name should pass, got %v", maxStageNameLen, err)
		}
		if err := validateWorkflow([]StageInput{stage(strings.Repeat("x", maxStageNameLen+1))}); !errors.Is(err, ErrInvalidStage) {
			t.Fatalf("expected ErrInvalidStage, got %v", err)
		}
	})

	t.Run("component config is still validated", func(t *testing.T) {
		bad := helmNode(uuid.New())
		bad.TargetNamespace = ""
		if err := validateWorkflow([]StageInput{stage("deploy", bad)}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("expected ErrInvalidConfig, got %v", err)
		}
	})
}
