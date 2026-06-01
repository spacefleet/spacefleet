# Testing

How we test Spacefleet, and where to add new tests. The goal is **confidence
per unit of maintenance** — lean on what the architecture already guarantees,
and weight effort toward the seams where bugs actually live.

## What the architecture gives us for free

- **The OpenAPI contract + Go's type system.** A handler that doesn't match
  `api/openapi.yaml` won't compile (`StrictServerInterface`), and the TS client
  is generated from the same spec. That's contract enforcement we don't write.
- **A test verifier.** Production has no passthrough — `RequireAuth` fails
  closed and the server won't boot without bundled-Dex OIDC config. Tests inject
  `testsupport.FakeVerifier`, which maps the bearer token to the user's subject,
  so we can test *features* (and per-user isolation) without standing up Dex, and
  reserve real-Dex tests for testing *auth itself*. Keep those two concerns
  separate.

## The layers

| Layer | Tool | Runs against | When to add |
| --- | --- | --- | --- |
| **Go unit** | `go test` | nothing external | Pure / branchy logic: the OIDC verifier, `config.Load`, domain rules. Fast, no deps. |
| **Go integration** | `go test -tags=integration` | real ephemeral Postgres | The bulk. Drive the full HTTP handler tree (or a service) against a real DB + migrations, with passthrough auth. |
| **Frontend unit** | Vitest + RTL | jsdom | Client logic worth isolating: hooks, guards, form/state behavior. Not thin presentational components. |
| **E2E** | Playwright | the running app + real Dex | A few critical journeys only — chiefly the auth flow. Expensive to maintain; keep it small. |

Weight: most coverage in **Go integration**, a thin shell of **Go unit**, a
handful of **frontend unit** tests where logic warrants, and a **small** E2E
suite for cross-cutting journeys.

## Go unit tests

Plain `_test.go` next to the code, no build tag. Examples today:
`lib/auth/middleware_test.go`, `lib/config/...`, `lib/server/routes_test.go`
(the route/auth smoke test — deliberately DB-free, uses a nil service).

```sh
make test          # go test ./...  (unit only — integration is tag-gated)
```

## Go integration tests

Tagged `//go:build integration` so the default `go test ./...` stays fast and
dependency-free. The harness ([lib/testsupport](lib/testsupport)) gives each
test an **isolated Postgres database**: it creates a uniquely-named DB, applies
`db/migrations`, hands back an `*ent.Client`, and drops the DB on cleanup. If
Postgres isn't reachable the test **skips** with a clear message rather than
failing — so contributors without services up aren't blocked, but CI (which
runs services) gets full coverage.

```sh
make services-up           # Postgres must be running
make test-integration      # go test -tags=integration ./...
```

Pattern (see [lib/server/integration_test.go](lib/server/integration_test.go)):

```go
//go:build integration

client := testsupport.NewEntClient(t)            // isolated DB, migrated
h := buildHandler(cfg, notes.NewService(client), nil) // nil verifier = passthrough
// ...exercise /api/* over httptest and assert against the real DB
```

Override the base DB with `TEST_DATABASE_URL` (defaults to the compose DSN).

## Frontend unit tests (Vitest + React Testing Library)

```sh
cd ui && npm test            # vitest run
cd ui && npm run test:watch  # watch mode
```

Config lives in [ui/vite.config.ts](ui/vite.config.ts) (`test` block, jsdom
env); setup in [ui/src/test/setup.ts](ui/src/test/setup.ts). Mock
`react-oidc-context` to drive auth state. Example:
[ui/src/components/AuthGate.test.tsx](ui/src/components/AuthGate.test.tsx).

## E2E (Playwright)

One real-browser journey covering the **auth flow** — login via Dex → lands on
Home (not NotFound) → an authenticated API call renders → sign out. This is the
regression guard for cross-cutting bugs that unit/integration tests can't see
(e.g. the post-login callback routing bug).

Requires the full stack: Postgres + **Dex** up, migrations applied.
The Playwright config starts the Go API and Vite dev server (reusing them if
already running), so locally you can just have `make dev` + `make ui-dev`
going.

```sh
make services-up && make migrate-up
make e2e                     # cd ui && npx playwright test
```

Tests live in [ui/e2e](ui/e2e); config in
[ui/playwright.config.ts](ui/playwright.config.ts).

## CI sketch

1. `make test` — fast unit gate.
2. `make services-up && make migrate-up && make test-integration`.
3. `cd ui && npm run typecheck && npm test`.
4. `make e2e` (services already up) — last, slowest.
