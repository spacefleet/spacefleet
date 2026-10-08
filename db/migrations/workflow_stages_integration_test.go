//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/db/migrations"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/migrate"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// stagesMigration is the first migration of the move to workflow stages; the
// test seeds the old graph shape just before it.
const stagesMigration = "20261008120000_workflow_stages.sql"

// migrationsBefore is the migration set up to (not including) name.
func migrationsBefore(t *testing.T, name string) fs.FS {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, n := range names {
		if n >= name {
			continue
		}
		b, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = &fstest.MapFile{Data: b}
	}
	return out
}

// legacyComponent is one row of the old graph shape.
type legacyComponent struct {
	id        uuid.UUID
	name      string
	dependsOn []uuid.UUID
	x         float64
	groupID   *uuid.UUID
}

func insertGroup(t *testing.T, db *sql.DB, orgID, appID, id uuid.UUID, name string, dependsOn []uuid.UUID, x float64) {
	t.Helper()
	deps, _ := json.Marshal(dependsOn)
	pos, _ := json.Marshal(map[string]float64{"x": x, "y": 0})
	if _, err := db.Exec(`INSERT INTO component_groups (id, organization_id, application_id, name, depends_on, position, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())`, id, orgID, appID, name, string(deps), string(pos)); err != nil {
		t.Fatalf("insert group %q: %v", name, err)
	}
}

func insertComponents(t *testing.T, db *sql.DB, orgID, appID uuid.UUID, comps ...legacyComponent) {
	t.Helper()
	for i, c := range comps {
		deps := c.dependsOn
		if deps == nil {
			deps = []uuid.UUID{}
		}
		depsJSON, _ := json.Marshal(deps)
		pos, _ := json.Marshal(map[string]float64{"x": c.x, "y": 0})
		// created_at increases with insertion order, the tie-break after x.
		if _, err := db.Exec(`INSERT INTO components (id, organization_id, application_id, name, type, config, depends_on, position, group_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'manifest', '{}'::jsonb, $5, $6, $7, now() + make_interval(secs => $8), now())`,
			c.id, orgID, appID, c.name, string(depsJSON), string(pos), c.groupID, i); err != nil {
			t.Fatalf("insert component %q: %v", c.name, err)
		}
	}
}

// TestWorkflowStagesBackfill seeds workflows in the old graph shape — plain
// edges, a diamond, group containers referenced from both sides, a group
// spanning two depths, a dangling edge, JSON-null and junk values, and an app
// with no components — then
// runs the stage migrations and checks every component landed in the stage of
// its longest dependency chain, ordered left to right, with group names kept
// only where a stage is exactly one group, and the old columns gone.
func TestWorkflowStagesBackfill(t *testing.T) {
	sqlDB, client := testsupport.NewDatabase(t)
	ctx := context.Background()
	if _, err := migrate.New(sqlDB, migrationsBefore(t, stagesMigration)).Up(ctx); err != nil {
		t.Fatalf("migrate to before stages: %v", err)
	}

	org, err := client.Organization.Create().SetName("Acme").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := client.Cluster.Create().SetOrganizationID(org.ID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newApp := func(name string) uuid.UUID {
		app, err := client.Application.Create().SetOrganizationID(org.ID).SetName(name).SetRunnerClusterID(runner.ID).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return app.ID
	}

	// App "shop": cache + db → migrate (needs db) → api (needs migrate and
	// cache) → the "frontends" group (web, admin; the group needs api) → smoke
	// (needs the group).
	shop := newApp("shop")
	cache, db, mig, api, web, admin, smoke := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	frontends := uuid.New()
	insertGroup(t, sqlDB, org.ID, shop, frontends, "frontends", []uuid.UUID{api}, 400)
	insertComponents(t, sqlDB, org.ID, shop,
		legacyComponent{id: db, name: "db", x: 300},
		legacyComponent{id: cache, name: "cache", x: 100},
		legacyComponent{id: mig, name: "migrate", dependsOn: []uuid.UUID{db}},
		legacyComponent{id: api, name: "api", dependsOn: []uuid.UUID{mig, cache}},
		// Positions inside a group are relative to it: admin sits left of web.
		legacyComponent{id: web, name: "web", x: 50, groupID: &frontends},
		legacyComponent{id: admin, name: "admin", x: 10, groupID: &frontends},
		legacyComponent{id: smoke, name: "smoke", dependsOn: []uuid.UUID{frontends}},
	)

	// App "split": one group whose members sit at two depths (so its name fits
	// neither stage), and a component with a dangling edge (ignored).
	split := newApp("split")
	mixed := uuid.New()
	m1, m2, lone := uuid.New(), uuid.New(), uuid.New()
	insertGroup(t, sqlDB, org.ID, split, mixed, "mixed", nil, 0)
	insertComponents(t, sqlDB, org.ID, split,
		legacyComponent{id: m1, name: "m1", groupID: &mixed},
		legacyComponent{id: m2, name: "m2", dependsOn: []uuid.UUID{m1}, groupID: &mixed},
		legacyComponent{id: lone, name: "lone", dependsOn: []uuid.UUID{uuid.New()}, x: 500},
	)
	// Rows as older code paths could leave them: a JSON-null depends_on and
	// position, and a depends_on entry that isn't a uuid. Both read as no
	// dependencies; with no x they sort after the positioned components.
	for i, odd := range []struct{ name, deps, pos string }{
		{"nulls", "null", "null"},
		{"junk", `["not-a-uuid"]`, `{"y": 1}`},
	} {
		if _, err := sqlDB.Exec(`INSERT INTO components (id, organization_id, application_id, name, type, depends_on, position, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'manifest', $5::jsonb, $6::jsonb, now() + make_interval(secs => $7), now())`,
			uuid.New(), org.ID, split, odd.name, odd.deps, odd.pos, 10+i); err != nil {
			t.Fatalf("insert %s: %v", odd.name, err)
		}
	}

	// App "empty": no components, so no stages.
	empty := newApp("empty")

	if _, err := migrate.New(sqlDB, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("migrate through stages: %v", err)
	}

	svc := workflows.NewService(client)
	layout := func(appID uuid.UUID) []string {
		t.Helper()
		stages, err := svc.GetWorkflow(ctx, org.ID, appID)
		if err != nil {
			t.Fatalf("GetWorkflow: %v", err)
		}
		var out []string
		for i, st := range stages {
			if st.Ordinal != i {
				t.Errorf("stage %q ordinal = %d, want %d", st.Name, st.Ordinal, i)
			}
			names := make([]string, len(st.Edges.Components))
			for j, c := range st.Edges.Components {
				names[j] = c.Name
				if c.Ordinal != j {
					t.Errorf("component %q ordinal = %d, want %d", c.Name, c.Ordinal, j)
				}
			}
			out = append(out, st.Name+": "+strings.Join(names, ","))
		}
		return out
	}
	check := func(app string, got, want []string) {
		t.Helper()
		if strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("%s stages:\n got  %v\n want %v", app, got, want)
		}
	}
	check("shop", layout(shop), []string{
		"Stage 1: cache,db",
		"Stage 2: migrate",
		"Stage 3: api",
		"frontends: admin,web",
		"Stage 5: smoke",
	})
	check("split", layout(split), []string{
		"Stage 1: m1,lone,nulls,junk",
		"Stage 2: m2",
	})
	check("empty", layout(empty), nil)

	// The graph columns and the groups table are gone.
	var n int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'components' AND column_name IN ('depends_on', 'position', 'group_id')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("old component columns remaining = %d (err %v), want 0", n, err)
	}
	if err := sqlDB.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'component_groups'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("component_groups remaining = %d (err %v), want 0", n, err)
	}

	// The backfilled workflow runs in stage order: a run of shop desugars to
	// smoke waiting on both frontends.
	run, err := svc.BeginRun(ctx, org.ID, shop, workflows.ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var snap workflows.GraphSnapshot
	if err := json.Unmarshal([]byte(run.Graph), &snap); err != nil {
		t.Fatal(err)
	}
	for _, node := range snap.Nodes {
		if node.ID != smoke {
			continue
		}
		if len(node.DependsOn) != 2 || node.DependsOn[0] != admin || node.DependsOn[1] != web {
			t.Errorf("smoke deps = %v, want [admin web]", node.DependsOn)
		}
	}
	if len(snap.Stages) != 5 || snap.Stages[3].Name != "frontends" {
		t.Errorf("snapshot stages = %+v", snap.Stages)
	}

	// Every component kept its id, so nothing keyed on it (variables, state,
	// run history) is orphaned.
	if got, err := client.Component.Query().Count(ctx); err != nil || got != 12 {
		t.Errorf("components = %d (err %v), want 12", got, err)
	}
	if _, err := client.Component.Get(ctx, smoke); err != nil {
		t.Errorf("smoke lost its id: %v", err)
	}
}
