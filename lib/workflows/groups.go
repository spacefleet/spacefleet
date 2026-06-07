package workflows

import (
	"fmt"

	"github.com/google/uuid"
)

// GroupInput is one explicit group container of a proposed workflow, as supplied
// by the canvas. ID is client-provided (a stable uuid per group) so component
// group_id refs and depends_on edges survive a replace. DependsOn entries may
// reference a component id OR a sibling group id; expandDependencies desugars
// them into pure component-level edges. There are no nested groups, so a group
// has no group_id.
type GroupInput struct {
	ID        uuid.UUID
	Name      string
	DependsOn []uuid.UUID
	Position  map[string]float64
	Size      map[string]float64
}

// expandDependencies turns the authored graph (components with depends_on that
// may reference component OR group ids, each component an optional group_id; and
// groups each with their own depends_on of component/group ids) into a pure
// component-level dependency map the scheduler can consume.
//
// For each component C, its expanded deps =
//
//	expand( C.depends_on ∪ (C.group_id != nil ? group[C.group_id].depends_on : ∅) )
//
// where expand(refs) walks each ref id: a component id is included directly; a
// group id is replaced by all member component ids of that group (components
// whose group_id == that group). C itself is then removed and the result is
// deduped. This realizes the "all members must complete" semantics: depending on
// a group means waiting on every member; a group depending on Y makes every
// member wait on Y. Members of the same group gain no edge between them (they run
// in parallel).
//
// It is pure (no ent, no I/O) so it is unit tested without a database. Unknown
// refs are tolerated here (skipped); validateWorkflow rejects them first.
func expandDependencies(components []ComponentInput, groups []GroupInput) map[uuid.UUID][]uuid.UUID {
	// members[g] = component ids whose group_id == g.
	members := make(map[uuid.UUID][]uuid.UUID, len(groups))
	for _, c := range components {
		if c.GroupID != nil && *c.GroupID != uuid.Nil {
			members[*c.GroupID] = append(members[*c.GroupID], c.ID)
		}
	}
	groupDeps := make(map[uuid.UUID][]uuid.UUID, len(groups))
	groupExists := make(map[uuid.UUID]struct{}, len(groups))
	for _, g := range groups {
		groupDeps[g.ID] = g.DependsOn
		groupExists[g.ID] = struct{}{}
	}

	// expand resolves one ref id to component ids, appending into out (deduped via
	// seen). A group ref expands to its members; a component ref is itself.
	expand := func(ref uuid.UUID, out *[]uuid.UUID, seen map[uuid.UUID]struct{}) {
		if _, isGroup := groupExists[ref]; isGroup {
			for _, m := range members[ref] {
				if _, ok := seen[m]; !ok {
					seen[m] = struct{}{}
					*out = append(*out, m)
				}
			}
			return
		}
		if _, ok := seen[ref]; !ok {
			seen[ref] = struct{}{}
			*out = append(*out, ref)
		}
	}

	result := make(map[uuid.UUID][]uuid.UUID, len(components))
	for _, c := range components {
		var deps []uuid.UUID
		seen := make(map[uuid.UUID]struct{})
		// Refs authored directly on the component.
		for _, ref := range c.DependsOn {
			expand(ref, &deps, seen)
		}
		// Plus the refs inherited from the component's group (a group's depends_on
		// applies to every member).
		if c.GroupID != nil && *c.GroupID != uuid.Nil {
			for _, ref := range groupDeps[*c.GroupID] {
				expand(ref, &deps, seen)
			}
		}
		// A node never depends on itself (e.g. it expanded a group it belongs to, or
		// a self ref slipped through).
		filtered := deps[:0]
		for _, d := range deps {
			if d != c.ID {
				filtered = append(filtered, d)
			}
		}
		result[c.ID] = filtered
	}
	return result
}

// validateWorkflow validates the authored graph (components + group containers)
// and returns nil when it is a well-formed, acyclic DAG with valid per-type
// config. It checks: every component id is distinct and non-zero (reusing the
// component-level rules); every component's group_id (if set) names an existing
// group; every depends_on entry (on components AND groups) names an existing
// component or group; a group does not depend on itself; then it expands the
// authored refs into the pure component-level graph and runs the existing cycle
// + per-type config checks on that expanded graph (so a cycle that only forms
// through groups is caught). It is pure (no ent, no I/O).
func validateWorkflow(components []ComponentInput, groups []GroupInput) error {
	// Component ids: distinct + non-zero.
	compIDs := make(map[uuid.UUID]struct{}, len(components))
	for _, c := range components {
		if c.ID == uuid.Nil {
			return fmt.Errorf("%w: node %q", ErrMissingID, c.Name)
		}
		if _, dup := compIDs[c.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateID, c.ID)
		}
		compIDs[c.ID] = struct{}{}
	}
	// Group ids: distinct + non-zero, and disjoint from component ids.
	groupIDs := make(map[uuid.UUID]struct{}, len(groups))
	for _, g := range groups {
		if g.ID == uuid.Nil {
			return fmt.Errorf("%w: group %q", ErrMissingID, g.Name)
		}
		if _, dup := groupIDs[g.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateID, g.ID)
		}
		if _, clash := compIDs[g.ID]; clash {
			return fmt.Errorf("%w: %s is both a component and a group", ErrDuplicateID, g.ID)
		}
		groupIDs[g.ID] = struct{}{}
	}

	// knownRef reports whether an id names an existing component or group.
	knownRef := func(id uuid.UUID) bool {
		if _, ok := compIDs[id]; ok {
			return true
		}
		_, ok := groupIDs[id]
		return ok
	}

	// Every component's group_id must name an existing group; every depends_on ref
	// must name an existing component or group.
	for _, c := range components {
		if c.GroupID != nil && *c.GroupID != uuid.Nil {
			if _, ok := groupIDs[*c.GroupID]; !ok {
				return fmt.Errorf("%w: node %q references group %s", ErrUnknownGroup, c.Name, *c.GroupID)
			}
		}
		for _, dep := range c.DependsOn {
			if dep == c.ID {
				return fmt.Errorf("%w: %s (%q)", ErrSelfDependency, c.ID, c.Name)
			}
			if !knownRef(dep) {
				return fmt.Errorf("%w: node %q depends on %s", ErrUnknownDependency, c.Name, dep)
			}
		}
	}

	// Group depends_on refs: must resolve, and a group may not depend on itself.
	// Nested-group membership is impossible by construction (a group has no
	// group_id), so there is nothing further to guard there.
	for _, g := range groups {
		for _, dep := range g.DependsOn {
			if dep == g.ID {
				return fmt.Errorf("%w: group %s (%q)", ErrSelfDependency, g.ID, g.Name)
			}
			if !knownRef(dep) {
				return fmt.Errorf("%w: group %q depends on %s", ErrUnknownDependency, g.Name, dep)
			}
		}
	}

	// Per-type config is a property of the component alone — validate it directly.
	for _, c := range components {
		if err := validateConfig(c); err != nil {
			return err
		}
	}

	// Build the expanded component-level graph and run the cycle check on it, so a
	// cycle that only forms by routing through a group is caught.
	expanded := expandDependencies(components, groups)
	if err := detectCycleAdj(expanded); err != nil {
		return err
	}
	return nil
}
