# CLAUDE.md

Spacefleet is a Go backend + React SPA that ship as a single binary. The Go
program serves `/api/*` and the embedded Vite build from the same origin in
production. A shared OpenAPI spec drives both the server stubs and the
TypeScript client.

This codebase is a clean starting point (stripped from an earlier, larger
app): Go + Postgres (ent) + Redis + a React/Vite/Tailwind SPA, with an
OpenAPI-driven contract, Dex (OIDC) authentication, and an example `notes`
resource wired end-to-end. Tests span Go unit/integration, frontend unit
(Vitest), and browser e2e (Playwright) — see [TESTING.md](TESTING.md).

**This is an open-source project** (Apache-2.0, see [LICENSE](LICENSE)). The
entire platform is open source, and it may be self-hosted by anyone, so nothing
in here may assume or hardcode "we are the ones running it" — keep
operator-specific assumptions out of the codebase.

If a CLAUDE.custom.md file exists in the root of the project, read that first
and then continue with the rest of this document.

## Architecture essentials

- **Entrypoint**: [cmd/spacefleet/main.go](cmd/spacefleet/main.go) dispatches by subcommand — `serve` (HTTP API, the default), `worker` (River background jobs), `migrate` (SQL migrations).
- **HTTP server**: built in [lib/server/server.go](lib/server/server.go) (wires Postgres+ent and Redis), routed in [lib/server/routes.go](lib/server/routes.go).
- **Routing** mounts three things on one `*http.ServeMux`:
  1. Generated `/api/*` handlers behind the `RequireAuth` middleware.
  2. `/config.js` — emits `window.appConfig` with non-secret OIDC values.
  3. `/` → [ui/embed.go](ui/embed.go), the embedded SPA, with `index.html` fallback for client-side routing.
- **Auth**: **Dex (OIDC)**, behind a seam in [lib/auth](lib/auth). `RequireAuth` takes a `TokenVerifier`; when `OIDC_ISSUER` is set, [server.go](lib/server/server.go) builds an OIDC verifier ([lib/auth/oidc.go](lib/auth/oidc.go)) that validates Dex-issued **ID tokens** (signature via JWKS, `iss`/`exp`/`aud`) and passes it in [routes.go](lib/server/routes.go). When `OIDC_ISSUER` is empty, it falls back to a **dev passthrough** that accepts every request as `dev-user` (NEVER use in production). `publicAPIPaths` lists the bypass paths (`/api/health`). Dex itself runs in Docker Compose, bootstrapped from [dev/dex/config.yaml](dev/dex/config.yaml).
- **Frontend**: Vite + React 18 + TS, React Router v7, Tailwind v4. The typed API client is in [ui/src/api/client.ts](ui/src/api/client.ts). Login uses `react-oidc-context` (Authorization Code + PKCE, public client): `AuthProvider` is configured in [main.tsx](ui/src/main.tsx) from `window.appConfig`, `AuthGate` redirects unauthenticated users to Dex, and `ApiAuthBinder` feeds the ID token to the API client as the bearer token.

## The OpenAPI contract is the source of truth

[api/openapi.yaml](api/openapi.yaml) generates:
- [lib/api/gen.go](lib/api/gen.go) (Go types + `StrictServerInterface`) via `oapi-codegen` (config in [lib/api/cfg.yaml](lib/api/cfg.yaml), invoked by `go:generate` in [lib/api/doc.go](lib/api/doc.go)).
- [ui/src/api/schema.d.ts](ui/src/api/schema.d.ts) via `openapi-typescript`.

Workflow for a new/changed endpoint:
1. Edit `api/openapi.yaml`.
2. Run `make gen`.
3. Implement the new method on `*Server` in [lib/api/handlers.go](lib/api/handlers.go). The build breaks until you do — that's the gate.
4. Call it from the UI via `api.GET("/api/...")` — types flow through automatically.

Never edit `gen.go` or `schema.d.ts` by hand.

## Database (ent + SQL migrations)

- Schemas live in [ent/schema](ent/schema); `make gen` (`go generate ./ent/...`) regenerates the client. Never edit generated `ent/*.go` by hand.
- Runtime migrations are hand-written SQL in [db/migrations](db/migrations), applied by the `migrate` subcommand (atlas-style `YYYYMMDDHHMMSS_name.sql` filenames, tracked in `schema_migrations`). Adding/changing an ent schema means writing a matching SQL migration.
- [lib/db/db.go](lib/db/db.go) opens one pgx pool shared by ent and the migrator.

## Two processes: `serve` and `worker`

`spacefleet serve` runs the stateless HTTP API (scale horizontally).
`spacefleet worker` runs the River background-job worker — today it's
scaffolding with an empty registry; register jobs in [lib/queue](lib/queue)
and wire them in [cmd/spacefleet/worker.go](cmd/spacefleet/worker.go). Both
read the same `.env`.

## Deployment (Helm + GHCR)

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) publishes two
artifacts on every `v*` tag, behind the full lint/test gate: the multi-arch
image (`ghcr.io/spacefleet/app:X.Y.Z`) and the Helm chart as an OCI artifact
(`oci://ghcr.io/spacefleet/charts/spacefleet`, version `X.Y.Z`). The chart's
`version`/`appVersion` are stamped from the tag at package time — the committed
`Chart.yaml` carries `0.0.0` placeholders.

The chart ([deploy/charts/spacefleet](deploy/charts/spacefleet)) deploys `serve`
(web) + `worker`, runs `migrate up` as a `post-install,pre-upgrade` hook Job
(post-install, not pre-, so it can reach the bundled Postgres), and builds
`DATABASE_URL`/`REDIS_URL` into a Secret. Postgres + Redis are bundled as small
**first-party StatefulSets running the official upstream images** (the same
`postgres:17-alpine`/`redis:7-alpine` as docker-compose) — no third-party chart
dependencies. On by default for one-command trials; disable + use
`externalDatabase`/`externalRedis` for prod. `config.oidc.issuer` is empty by
default → backend runs the dev passthrough; set it for real deployments.

When changing chart templates, run `make helm-lint` — the `lint-helm` CI job
gates the same way the Go/UI linters do. The chart has no external dependencies,
so there's no `helm dependency`/`Chart.lock` step.

## UI components

shadcn/ui is welcome as a starting point — the project is scaffolded for it
(`@/*` alias, `cn()` in [ui/src/lib/utils.ts](ui/src/lib/utils.ts),
`lucide-react`, [ui/components.json](ui/components.json)). Add with
`cd ui && npx shadcn add <name>` (lands in `ui/src/components/ui/`).

**Brand: sharp corners, no border radius.** Don't add `rounded-*` to new
rectangular components; the Tailwind radius scale is overridden to zero in
[ui/src/index.css](ui/src/index.css) as a safety net. `rounded-full` is fine.

## Dev workflow

```sh
make services-up   # Postgres + Redis + Dex (OIDC) on :5556
make migrate-up    # apply migrations
make dev           # Go backend on :8080 (Air live-reload)
make ui-dev        # Vite on :2424, proxies /api/* and /config.js to :8080
```

Open <http://localhost:2424>. You'll be redirected to Dex to log in — the dev
login is **`admin@example.com` / `password`** (seeded in
[dev/dex/config.yaml](dev/dex/config.yaml)). To skip auth entirely, clear
`OIDC_ISSUER` in `.env` and the backend reverts to the dev passthrough.

The app↔API is same-origin in dev (Vite proxy) and prod (embedded binary) — no
CORS there. The SPA↔Dex calls (discovery, JWKS, token) *are* cross-origin, so
Dex's `web.allowedOrigins` must list the app origins (`:2424`, `:8080`).

## Common commands

| Task | Command |
| --- | --- |
| Regenerate ent + Go + TS | `make gen` |
| Go unit tests | `make test` |
| Go integration tests (real Postgres) | `make test-integration` |
| UI unit tests (Vitest) | `cd ui && npm test` |
| Browser e2e (Playwright) | `make e2e` |
| Go vet / fmt | `make vet` / `make fmt` |
| UI typecheck | `cd ui && npm run typecheck` |
| Production build | `make build` (UI → `ui/dist` → embedded → `bin/spacefleet`) |
| Apply migrations | `make migrate-up` |
| Lint/render Helm chart | `make helm-lint` / `make helm-template` |

See [TESTING.md](TESTING.md) for the testing strategy (layers, when to use
which, and how the harnesses work).

## Gotchas

- **Empty `ui/dist` breaks Go builds.** `//go:embed all:dist` needs at least one file. `make ui-build` keeps a `.gitkeep`; if you wiped `ui/dist/`, run `make ui-build` before `go build`.
- **Middleware order is reversed.** `oapi-codegen` applies the `Middlewares` slice last-to-first, so the *last* entry wraps outermost.
- **`window.appConfig` only ships non-secrets.** Anything added to `appConfigHandler` is visible to every browser.
- **New `/api/*` routes need `make gen` first.** If a request returns HTML, the route isn't mounted — you forgot to regenerate or didn't register the handler.
- **Air's `exclude_dir` skips `ui/`.** Changing TS/TSX won't restart the Go server — Vite HMR handles the UI side.
- **Dex reads its config only at startup.** After editing [dev/dex/config.yaml](dev/dex/config.yaml), `make services-up` won't pick it up (the service definition is unchanged) — run `docker compose restart dex`.
- **The UI dev port (`2424`) is pinned in three places.** [vite.config.ts](ui/vite.config.ts) (`strictPort`), and Dex's `redirectURIs` + `allowedOrigins`. Changing it means updating all of them, then restarting Dex.
- **Don't clean up the OIDC callback URL with raw `history.replaceState`.** It desyncs React Router (URL changes, router doesn't), landing you on NotFound. The `/auth/callback` route ([ui/src/routes/AuthCallback.tsx](ui/src/routes/AuthCallback.tsx)) navigates home *through the router* instead.
- **Integration tests are tag-gated.** `make test` runs unit tests only; real-Postgres tests need `make test-integration` (build tag `integration`).

## The example `notes` resource

`Note` (ent schema, migration, `/api/notes` endpoints in
[lib/api/handlers.go](lib/api/handlers.go) + [lib/notes](lib/notes), and the
`Home` page) exists only to demonstrate the full data path. Delete it once you
have real resources.

## Project layout

```
spacefleet-app/
├── api/openapi.yaml         # shared contract (drives Go + TS)
├── cmd/spacefleet/          # main.go (subcommand dispatch) + serve.go + worker.go + migrate.go
├── db/migrations/           # hand-written SQL migrations
├── deploy/charts/spacefleet # Helm chart (serve+worker+migrate, optional bundled PG/Redis) — published to GHCR as OCI on v* tags
├── dev/dex/config.yaml      # Dex (OIDC) bootstrap for local dev — static client + dev login
├── ent/                     # ent ORM: schema/ (hand-written) + generated client
├── lib/
│   ├── api/                 # gen.go (generated) + handlers.go (hand-written)
│   ├── auth/                # RequireAuth middleware + OIDC verifier (oidc.go) + dev passthrough
│   ├── cache/               # Redis client
│   ├── config/              # env loading
│   ├── db/                  # Postgres + ent wiring
│   ├── migrate/             # SQL migration runner
│   ├── notes/               # example domain service
│   ├── queue/               # River wrapper (worker registry, migrations)
│   ├── server/              # http.Server, request logging, route mounting
│   └── testsupport/         # integration-test harness (isolated Postgres per test)
├── ui/
│   ├── embed.go             # //go:embed all:dist
│   ├── e2e/                 # Playwright browser tests (auth journey)
│   ├── playwright.config.ts # e2e config (starts/reuses API + Vite dev server)
│   ├── src/api/             # generated schema + openapi-fetch client
│   ├── src/components/      # ApiAuthBinder, AuthGate, Layout (+ *.test.tsx unit tests)
│   ├── src/routes/          # page-level components (Home, NotFound, AuthCallback)
│   ├── src/test/            # Vitest setup
│   └── vite.config.ts       # dev server (:2424) /api + /config.js proxy; Vitest config
├── Makefile
├── docker-compose.yml       # Postgres + Redis + Dex for local dev
├── Dockerfile               # multi-stage → distroless single binary
├── TESTING.md               # testing strategy + how each layer works
└── .air.toml
```
