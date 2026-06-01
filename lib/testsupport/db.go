//go:build integration

package testsupport

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/spacefleet/app/db/migrations"
	"github.com/spacefleet/app/ent"
	"github.com/spacefleet/app/lib/db"
	"github.com/spacefleet/app/lib/migrate"
)

// defaultBaseDSN matches the docker-compose Postgres (see docker-compose.yml).
const defaultBaseDSN = "postgres://spacefleet:spacefleet@localhost:5432/spacefleet?sslmode=disable"

// dbCounter keeps test database names unique within a single test binary run.
var dbCounter atomic.Int64

// NewEntClient returns an *ent.Client backed by a freshly created, migrated
// Postgres database dedicated to this test. The database is dropped during
// test cleanup.
//
// The base connection comes from TEST_DATABASE_URL, then DATABASE_URL, then
// the compose default. If Postgres is unreachable the test is SKIPPED (not
// failed) with a hint to run `make services-up`, so contributors without the
// stack up aren't blocked — CI runs the services and gets full coverage.
func NewEntClient(t *testing.T) *ent.Client {
	t.Helper()

	baseDSN := firstNonEmpty(os.Getenv("TEST_DATABASE_URL"), os.Getenv("DATABASE_URL"), defaultBaseDSN)

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Skipf("integration: cannot open Postgres (%v) — run `make services-up`", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Skipf("integration: Postgres not reachable (%v) — run `make services-up`", err)
	}

	// Unique per process + per call so parallel tests never collide.
	name := fmt.Sprintf("sf_test_%d_%d", os.Getpid(), dbCounter.Add(1))
	// CREATE DATABASE can't be parameterized; the name is built from trusted
	// integers, so interpolation is safe here.
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name)); err != nil {
		_ = admin.Close()
		t.Fatalf("integration: drop stale db: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		_ = admin.Close()
		t.Fatalf("integration: create db %q: %v", name, err)
	}

	testDSN, err := withDBName(baseDSN, name)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("integration: build test dsn: %v", err)
	}

	sqlDB, client, err := db.Open(testDSN)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("integration: open test db: %v", err)
	}

	if _, err := migrate.New(sqlDB, migrations.FS).Up(ctx); err != nil {
		_ = client.Close()
		_ = sqlDB.Close()
		_ = admin.Close()
		t.Fatalf("integration: migrate: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
		_ = sqlDB.Close()
		// Drop on a short fresh context — the test's may be cancelled.
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		if _, err := admin.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name)); err != nil {
			t.Logf("integration: failed to drop test db %q: %v", name, err)
		}
		_ = admin.Close()
	})

	return client
}

// withDBName returns dsn with its database (path) replaced by name.
func withDBName(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
