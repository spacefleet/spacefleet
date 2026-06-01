// Package migrations embeds the hand-written SQL migration files into the
// binary so `spacefleet migrate up` is self-contained — it does not depend on
// the files being present on disk relative to the working directory. This
// mirrors how the SPA is embedded (ui/embed.go): everything the single binary
// needs ships inside it.
//
// The migrator (lib/migrate) reads *.sql from this fs.FS. Adding a migration is
// just dropping a new YYYYMMDDHHMMSS_name.sql file next to the others; it is
// embedded automatically on the next build.
package migrations

import "embed"

// FS holds the migration files, named at the root of the FS (e.g.
// "20260530130000_accounts.sql").
//
//go:embed *.sql
var FS embed.FS
