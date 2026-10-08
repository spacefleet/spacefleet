package workflows

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/slug"
)

// StageInput is one proposed stage of a workflow, as supplied by the builder:
// its client-provided id (stable across edits), its display name, and its
// components in display order. The order of the stages is run order.
type StageInput struct {
	ID         uuid.UUID
	Name       string
	Components []ComponentInput
}

// maxStageNameLen bounds a stage's display name (in characters).
const maxStageNameLen = 100

// flattenStages returns every component of the workflow, in stage order and
// then display order within each stage.
func flattenStages(stages []StageInput) []ComponentInput {
	var out []ComponentInput
	for _, st := range stages {
		out = append(out, st.Components...)
	}
	return out
}

// stageDependencies desugars stage order into the component-level dependency
// map the scheduler consumes: every component depends on every component of the
// nearest earlier non-empty stage. An empty stage is skipped rather than cutting
// the chain, the first non-empty stage depends on nothing, and components of
// the same stage gain no edge between them — they run in parallel. Waiting on
// the previous stage alone is enough: that stage already waited on the one
// before it, so the ordering is transitive.
//
// It is pure (no ent, no I/O) so it is unit tested without a database.
func stageDependencies(stages []StageInput) map[uuid.UUID][]uuid.UUID {
	deps := make(map[uuid.UUID][]uuid.UUID)
	var prev []uuid.UUID
	for _, st := range stages {
		if len(st.Components) == 0 {
			continue
		}
		ids := make([]uuid.UUID, len(st.Components))
		for i, c := range st.Components {
			ids[i] = c.ID
			// Each component gets its own copy so no two share a backing array.
			deps[c.ID] = append([]uuid.UUID(nil), prev...)
		}
		prev = ids
	}
	return deps
}

// validateWorkflow validates a proposed workflow and returns nil when it is
// well formed: every stage and component id is non-zero and distinct (across
// both), every stage has a name of at most maxStageNameLen characters, every
// component name is a slug, every component's per-type config is valid, and
// every ${{ components.<name>.outputs.* }} reference names an OpenTofu
// component in an earlier stage. Stage order is the only ordering, so there is
// no cycle to detect. It is pure (no ent, no I/O).
func validateWorkflow(stages []StageInput) error {
	ids := make(map[uuid.UUID]struct{})
	for _, st := range stages {
		if st.ID == uuid.Nil {
			return fmt.Errorf("%w: stage %q", ErrMissingID, st.Name)
		}
		if _, dup := ids[st.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateID, st.ID)
		}
		ids[st.ID] = struct{}{}
		name := strings.TrimSpace(st.Name)
		if name == "" {
			return fmt.Errorf("%w: every stage needs a name", ErrInvalidStage)
		}
		if utf8.RuneCountInString(name) > maxStageNameLen {
			return fmt.Errorf("%w: stage name %q is longer than %d characters", ErrInvalidStage, name, maxStageNameLen)
		}
		for _, c := range st.Components {
			if c.ID == uuid.Nil {
				return fmt.Errorf("%w: component %q", ErrMissingID, c.Name)
			}
			// A component is referenced by name in ${{ components.<name>.outputs.* }}
			// and seeds the default Helm release name, so force it to a slug.
			if !slug.Valid(c.Name) {
				return fmt.Errorf("%w: component name %q %s", ErrInvalidConfig, c.Name, slug.Rule)
			}
			if _, dup := ids[c.ID]; dup {
				return fmt.Errorf("%w: %s", ErrDuplicateID, c.ID)
			}
			ids[c.ID] = struct{}{}
		}
	}

	components := flattenStages(stages)
	// Per-type config is a property of the component alone — validate it directly.
	for _, c := range components {
		if err := validateConfig(c); err != nil {
			return err
		}
	}

	// components.<name>.outputs.* references are a cross-component property (name
	// resolution + stage order), so they're validated over the same desugared
	// edges the run executes. The plan→apply structure an OpenTofu deployment
	// needs is not authored (it's synthesized per run by expandExecutionNodes),
	// so there is nothing else to check across components.
	return validateOutputRefs(components, stageDependencies(stages))
}
