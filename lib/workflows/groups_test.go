package workflows

import (
	"errors"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
)

// helmNodeIn builds a minimal valid helm node with the given id, group, and
// dependencies, so an expansion/validation test can focus on graph shape.
func helmNodeIn(id uuid.UUID, group *uuid.UUID, deps ...uuid.UUID) ComponentInput {
	return ComponentInput{
		ID:        id,
		Name:      "n-" + id.String()[:8],
		Type:      TypeHelm,
		GroupID:   group,
		DependsOn: deps,
		Config: map[string]string{
			helmConfigChartSource: helm.SourceOCI,
			helm.ConfigRepoURL:    "oci://example.com/charts/app",
		},
	}
}

// sortedEqual reports whether got and want hold the same id set (order-insensitive).
func sortedEqual(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]uuid.UUID(nil), got...)
	w := append([]uuid.UUID(nil), want...)
	sort.Slice(g, func(i, j int) bool { return g[i].String() < g[j].String() })
	sort.Slice(w, func(i, j int) bool { return w[i].String() < w[j].String() })
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// TestExpandDependencies_FanInViaGroup proves a node depending on a group waits
// for every member of that group (all-must-complete), and members gain no
// implicit edge between themselves.
func TestExpandDependencies_FanInViaGroup(t *testing.T) {
	g := uuid.New()
	m1, m2 := uuid.New(), uuid.New()
	x := uuid.New()
	comps := []ComponentInput{
		helmNodeIn(m1, &g),
		helmNodeIn(m2, &g),
		helmNodeIn(x, nil, g), // x depends on group g
	}
	groups := []GroupInput{{ID: g, Name: "grp"}}

	got := expandDependencies(comps, groups)
	if !sortedEqual(got[x], []uuid.UUID{m1, m2}) {
		t.Fatalf("x deps = %v, want {m1,m2}", got[x])
	}
	// Members run in parallel: no edge between them, and the group's own (empty)
	// depends_on adds nothing.
	if len(got[m1]) != 0 || len(got[m2]) != 0 {
		t.Fatalf("members should have no deps, got m1=%v m2=%v", got[m1], got[m2])
	}
}

// TestExpandDependencies_GroupDependsOn proves a group's depends_on is inherited
// by every member (each member waits on the group's refs).
func TestExpandDependencies_GroupDependsOn(t *testing.T) {
	y := uuid.New()
	g := uuid.New()
	m1, m2 := uuid.New(), uuid.New()
	comps := []ComponentInput{
		helmNodeIn(y, nil),
		helmNodeIn(m1, &g),
		helmNodeIn(m2, &g),
	}
	groups := []GroupInput{{ID: g, Name: "grp", DependsOn: []uuid.UUID{y}}}

	got := expandDependencies(comps, groups)
	if !sortedEqual(got[m1], []uuid.UUID{y}) || !sortedEqual(got[m2], []uuid.UUID{y}) {
		t.Fatalf("members should each depend on y; m1=%v m2=%v", got[m1], got[m2])
	}
	if len(got[y]) != 0 {
		t.Fatalf("y should have no deps, got %v", got[y])
	}
}

// TestExpandDependencies_GroupToGroup proves a group depending on another group
// expands to all members of that other group, for every member of the dependent
// group.
func TestExpandDependencies_GroupToGroup(t *testing.T) {
	gA, gB := uuid.New(), uuid.New()
	a1, a2 := uuid.New(), uuid.New()
	b1 := uuid.New()
	comps := []ComponentInput{
		helmNodeIn(a1, &gA),
		helmNodeIn(a2, &gA),
		helmNodeIn(b1, &gB),
	}
	// Group B depends on group A → every member of B waits on every member of A.
	groups := []GroupInput{
		{ID: gA, Name: "A"},
		{ID: gB, Name: "B", DependsOn: []uuid.UUID{gA}},
	}

	got := expandDependencies(comps, groups)
	if !sortedEqual(got[b1], []uuid.UUID{a1, a2}) {
		t.Fatalf("b1 deps = %v, want {a1,a2}", got[b1])
	}
}

// TestExpandDependencies_DropsSelf proves a member that ends up referencing its
// own group (so it would depend on itself) is filtered out.
func TestExpandDependencies_DropsSelf(t *testing.T) {
	g := uuid.New()
	m1, m2 := uuid.New(), uuid.New()
	comps := []ComponentInput{
		helmNodeIn(m1, &g, g), // m1 (in g) depends on g → would include m1 itself
		helmNodeIn(m2, &g),
	}
	groups := []GroupInput{{ID: g, Name: "grp"}}

	got := expandDependencies(comps, groups)
	if !sortedEqual(got[m1], []uuid.UUID{m2}) {
		t.Fatalf("m1 deps = %v, want {m2} (self removed)", got[m1])
	}
}

// TestValidateWorkflow_Valid proves a well-formed grouped workflow passes.
func TestValidateWorkflow_Valid(t *testing.T) {
	g := uuid.New()
	m1, m2 := uuid.New(), uuid.New()
	x := uuid.New()
	comps := []ComponentInput{
		helmNodeIn(m1, &g),
		helmNodeIn(m2, &g),
		helmNodeIn(x, nil, g),
	}
	groups := []GroupInput{{ID: g, Name: "grp"}}
	if err := validateWorkflow(comps, groups); err != nil {
		t.Fatalf("expected valid grouped workflow to pass, got %v", err)
	}
}

// TestValidateWorkflow_UnknownGroup proves a component referencing a missing
// group_id is rejected.
func TestValidateWorkflow_UnknownGroup(t *testing.T) {
	missing := uuid.New()
	c := helmNodeIn(uuid.New(), &missing)
	if err := validateWorkflow([]ComponentInput{c}, nil); !errors.Is(err, ErrUnknownGroup) {
		t.Fatalf("expected ErrUnknownGroup, got %v", err)
	}
}

// TestValidateWorkflow_UnknownGroupDep proves a depends_on naming neither a
// component nor a group is rejected.
func TestValidateWorkflow_UnknownGroupDep(t *testing.T) {
	missing := uuid.New()
	c := helmNodeIn(uuid.New(), nil, missing)
	if err := validateWorkflow([]ComponentInput{c}, nil); !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}
}

// TestValidateWorkflow_GroupSelfDependency proves a group depending on itself is
// rejected.
func TestValidateWorkflow_GroupSelfDependency(t *testing.T) {
	g := uuid.New()
	groups := []GroupInput{{ID: g, Name: "grp", DependsOn: []uuid.UUID{g}}}
	if err := validateWorkflow(nil, groups); !errors.Is(err, ErrSelfDependency) {
		t.Fatalf("expected ErrSelfDependency, got %v", err)
	}
}

// TestValidateWorkflow_CycleThroughGroups proves a cycle that only forms by
// routing through groups is caught by the cycle check on the expanded graph.
func TestValidateWorkflow_CycleThroughGroups(t *testing.T) {
	gA, gB := uuid.New(), uuid.New()
	a1 := uuid.New()
	b1 := uuid.New()
	comps := []ComponentInput{
		helmNodeIn(a1, &gA),
		helmNodeIn(b1, &gB),
	}
	// A depends on B and B depends on A → a1 ↔ b1 after expansion: a cycle.
	groups := []GroupInput{
		{ID: gA, Name: "A", DependsOn: []uuid.UUID{gB}},
		{ID: gB, Name: "B", DependsOn: []uuid.UUID{gA}},
	}
	if err := validateWorkflow(comps, groups); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle through groups, got %v", err)
	}
}
