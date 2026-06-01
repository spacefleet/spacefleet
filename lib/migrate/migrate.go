// Package migrate applies versioned SQL migrations from db/migrations and
// tracks applied versions in a schema_migrations table.
//
// Migration files follow the atlas convention (e.g. `20260424120000_name.sql`)
// so `ent` + `atlas migrate diff` can append to the directory without tooling
// on our side. We don't use atlas's `RevisionReadWriter`; a single
// schema_migrations table is plenty and avoids pulling in its full history
// model for what is today a ~2-table project.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	atlas "ariga.io/atlas/sql/migrate"
)

type Migrator struct {
	db   *sql.DB
	fsys fs.FS
}

// New returns a Migrator that reads *.sql migration files from fsys. Callers
// pass the embedded migrations FS (db/migrations) in production, so the binary
// is self-contained and does not depend on the working directory; tests may
// pass any fs.FS holding the same files.
func New(db *sql.DB, fsys fs.FS) *Migrator {
	return &Migrator{db: db, fsys: fsys}
}

// Up applies every pending migration in lexical order, in a transaction per
// file. Returns the versions it applied.
func (m *Migrator) Up(ctx context.Context) ([]string, error) {
	if err := m.ensureVersionsTable(ctx); err != nil {
		return nil, err
	}
	applied, err := m.appliedSet(ctx)
	if err != nil {
		return nil, err
	}
	files, err := m.orderedFiles()
	if err != nil {
		return nil, err
	}

	var done []string
	for _, f := range files {
		version := versionOf(f.Name())
		if _, ok := applied[version]; ok {
			continue
		}
		if err := m.applyOne(ctx, version, f); err != nil {
			return done, fmt.Errorf("apply %s: %w", f.Name(), err)
		}
		done = append(done, version)
	}
	return done, nil
}

// Status returns the sorted versions of applied and pending migrations.
func (m *Migrator) Status(ctx context.Context) (applied, pending []string, err error) {
	if err := m.ensureVersionsTable(ctx); err != nil {
		return nil, nil, err
	}
	appliedSet, err := m.appliedSet(ctx)
	if err != nil {
		return nil, nil, err
	}
	files, err := m.orderedFiles()
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		v := versionOf(f.Name())
		if _, ok := appliedSet[v]; ok {
			applied = append(applied, v)
		} else {
			pending = append(pending, v)
		}
	}
	return applied, pending, nil
}

func (m *Migrator) ensureVersionsTable(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func (m *Migrator) appliedSet(ctx context.Context) (map[string]struct{}, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func (m *Migrator) orderedFiles() ([]atlas.File, error) {
	names, err := fs.Glob(m.fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)
	files := make([]atlas.File, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(m.fsys, n)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", n, err)
		}
		files = append(files, atlas.NewLocalFile(n, b))
	}
	return files, nil
}

func (m *Migrator) applyOne(ctx context.Context, version string, f atlas.File) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(f.Bytes())); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// versionOf strips the `.sql` suffix, using the base filename as the version.
// Files like `20260424120000_init_cli_auth.sql` become
// `20260424120000_init_cli_auth`.
func versionOf(name string) string {
	return strings.TrimSuffix(filepath.Base(name), ".sql")
}
