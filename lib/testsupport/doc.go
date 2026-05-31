// Package testsupport holds helpers for integration tests — chiefly a
// Postgres harness that gives each test an isolated, migrated database.
//
// The actual helpers are behind the `integration` build tag (see db.go) so
// they're only compiled for `go test -tags=integration`. This file carries
// the package declaration so the package always has at least one buildable
// file under a normal `go build ./...`.
package testsupport
