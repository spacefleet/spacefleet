# CLAUDE.md

Spacefleet is a Go backend + React SPA that ship as a single binary. The Go
program serves `/api/*` and the embedded Vite build from the same origin in
production. A shared OpenAPI spec drives both the server stubs and the
TypeScript client.

The stack: Go + Postgres (ent) + Redis + a React/Vite/Tailwind SPA, with an
OpenAPI-driven contract and Dex (OIDC) authentication. The domain is
multi-tenant — users belong to **organizations** (via memberships) and most
resources are scoped to an org; **Kubernetes cluster registration** is the
first such resource and is the worked example for how a resource is built end
to end (see [How a resource is built](#how-a-resource-is-built)). Tests span Go
unit/integration, frontend unit (Vitest), and browser e2e (Playwright) — see
[TESTING.md](TESTING.md).

**This is an open-source project** (Apache-2.0, see [LICENSE](LICENSE)). The
entire platform is open source, and it may be self-hosted by anyone, so nothing
in here may assume or hardcode "we are the ones running it" — keep
operator-specific assumptions out of the codebase.

If a CLAUDE.custom.md file exists in the root of the project, read that first
and then continue with the rest of this document.

## Architecture essentials

- **Entrypoint**: [cmd/spacefleet/main.go](cmd/spacefleet/main.go) dispatches by subcommand — `serve` (HTTP API, the default), `worker` (River background jobs), `migrate` (SQL migrations).
- **HTTP server**: built in [lib/server/server.go](lib/server/server.go) (wires Postgres+ent, Redis, the credential sealer ([lib/secrets](lib/secrets)), and the domain services), routed in [lib/server/routes.go](lib/server/routes.go).
- **Routing** mounts three things on one `*http.ServeMux`:
  1. Generated `/api/*` handlers behind the `RequireAuth` middleware.
  2. `/config.js` — emits `window.appConfig` with non-secret OIDC values.
  3. `/` → [ui/embed.go](ui/embed.go), the embedded SPA, with `index.html` fallback for client-side routing.
- **Auth**: **Dex (OIDC)**, behind a seam in [lib/auth](lib/auth). `RequireAuth` takes a `TokenVerifier`; when `OIDC_ISSUER` is set, [server.go](lib/server/server.go) builds an OIDC verifier ([lib/auth/oidc.go](lib/auth/oidc.go)) that validates Dex-issued **ID tokens** (signature via JWKS, `iss`/`exp`/`aud`) and passes it in [routes.go](lib/server/routes.go). When `OIDC_ISSUER` is empty, it falls back to a **dev passthrough** that accepts every request as `dev-user` (NEVER use in production). `publicAPIPaths` lists the bypass paths (`/api/health`). Dex itself runs in Docker Compose, bootstrapped from [dev/dex/config.yaml](dev/dex/config.yaml). Operator-facing setup instructions (not code internals) live in [docs/operator/authentication.md](docs/operator/authentication.md) — see [End-user docs](#end-user-docs-docs).
- **Tenancy**: a second middleware, `OrgContext` ([lib/auth/org.go](lib/auth/org.go)), lifts the SPA's `X-Organization-ID` header onto the request context. It does **no** authorization — org-scoped handlers resolve the org and check the caller's membership themselves (`Server.currentOrg`). Auth runs outermost, then org resolution.
- **Frontend**: Vite + React 18 + TS, React Router v7, Tailwind v4. The typed API client is in [ui/src/api/client.ts](ui/src/api/client.ts). Login uses `react-oidc-context` (Authorization Code + PKCE, public client): `AuthProvider` is configured in [main.tsx](ui/src/main.tsx) from `window.appConfig`, `AuthGate` redirects unauthenticated users to Dex, and `ApiAuthBinder` feeds the ID token to the API client as the bearer token.

## The OpenAPI contract is the source of truth

[api/openapi.yaml](api/openapi.yaml) generates:
- [lib/api/gen.go](lib/api/gen.go) (Go types + `StrictServerInterface`) via `oapi-codegen` (config in [lib/api/cfg.yaml](lib/api/cfg.yaml), invoked by `go:generate` in [lib/api/doc.go](lib/api/doc.go)).
- [ui/src/api/schema.d.ts](ui/src/api/schema.d.ts) via `openapi-typescript`.

Workflow for a new/changed endpoint:
1. Edit `api/openapi.yaml`.
2. Run `make gen`.
3. Implement the new method on `*Server`. The build breaks until you do — that's the gate. Cross-cutting handlers (`GetHealth`, `GetMe`, organizations) live in [lib/api/handlers.go](lib/api/handlers.go); a resource gets its own file (e.g. [lib/api/clusters.go](lib/api/clusters.go)).
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

## End-user docs (`docs/`)

[docs/](docs) is product documentation for the **people who run and use
Spacefleet**, split by audience:

- **`docs/operator/`** — for whoever installs, configures, and operates a
  Spacefleet deployment (e.g. [install-with-helm.md](docs/operator/install-with-helm.md),
  [authentication.md](docs/operator/authentication.md)).
- **`docs/user/`** — for people using the running app (organizations, clusters,
  the features they interact with).

**These are not developer docs, and they are not this file.** Write them for a
reader who will *never* open the source and does not care how it's built — only
how to accomplish their task. Concretely:

- **No code internals.** Don't name Go/TS symbols (`RequireAuth`,
  `NewDevVerifier`, …), packages, function/middleware names, or describe request
  pipelines and code structure. Describe observable behavior and the actions the
  reader takes (settings, commands, UI steps).
- **No source links.** Never link into `lib/…`, `ui/src/…`, or other repo paths
  — the reader doesn't have a checkout. Link to other docs in `docs/`, to the
  provider/tool's own documentation, or give a command (`helm show values …`)
  instead.
- **Operator docs are about deploying/running** (Helm values, environment
  settings, identity-provider setup, troubleshooting from logs and `kubectl`);
  **user docs are about using the app** (what a feature does and how to use it).
  Local-from-source dev workflows (`make dev`, editing `dev/dex/config.yaml`,
  etc.) are *contributor* concerns — keep them out of `docs/`; they live here in
  CLAUDE.md and the README.
- **Self-hostable framing.** Per the open-source/self-host rule above, never
  assume "we" run the deployment — the reader might be running their own.

Architecture and implementation detail for *contributors* belong in this file
and inline in the code, not in `docs/`.

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

## How a resource is built

Every resource is wired through the same layers, in the same order. Clusters
([ent/schema/cluster.go](ent/schema/cluster.go), [lib/clusters](lib/clusters),
[lib/api/clusters.go](lib/api/clusters.go),
[ui/src/routes/Clusters.tsx](ui/src/routes/Clusters.tsx)) is the reference
implementation — copy its shape.

1. **ent schema** ([ent/schema](ent/schema)) — define the entity. Org-scoped
   resources carry an immutable `organization_id` field bound to an `edge.To`
   the `Organization`, plus an `index.Fields("organization_id", …)`. Mark
   credential fields `.Sensitive()`. Run `make gen`.
2. **SQL migration** ([db/migrations](db/migrations)) — hand-write the matching
   `CREATE TABLE` (the ent generator does *not* produce these). FK to
   `organizations(id) ON DELETE CASCADE` for org-scoped tables. Filename is
   `YYYYMMDDHHMMSS_name.sql`.
3. **OpenAPI** ([api/openapi.yaml](api/openapi.yaml)) — add the paths/schemas,
   `make gen`, implement the handler (see the contract section above).
4. **Service** ([lib/<resource>](lib)) — a thin, testable wrapper over the ent
   client (`NewService(entClient, …)`). Every query is **scoped by org id**
   (`Where(cluster.OrganizationID(orgID), …)`); the service never trusts an id
   alone. Domain logic (credential sealing via [lib/secrets](lib/secrets),
   probing via [lib/k8s](lib/k8s)) lives here, not in the handler.
5. **Handler** ([lib/api](lib/api)) — thin: resolve + authorize, call the
   service, map `*ent.X` → the API type. Org-scoped handlers start with the
   `resolveOrg` preamble (confirm services, resolve the user via
   `EnsureUser`, resolve + authorize the org via `currentOrg`). A `toAPIX`
   mapper converts ent rows to generated API types and **must never expose
   sealed/sensitive columns**. Use the `errResp[…]` generic for typed error
   bodies; map `ent.IsNotFound` → 404, `errNoOrg` → 400, non-membership → 403.
6. **UI** ([ui/src/routes](ui/src/routes)) — a page component calling
   `api.GET/POST("/api/…")`; types flow from the generated schema. The selected
   org is sent automatically as `X-Organization-ID` by the client middleware.

Conventions that hold across resources:

- **Services may be nil.** `NewServer` accepts nil services so route-level tests
  run without a database; a handler whose service is missing returns a clear
  "not configured" (503) rather than panicking.
- **Tenancy is enforced in the service query, not just the handler.** Scoping
  every `Where` by org id is the actual security boundary; the handler's
  membership check is the gate in front of it.
- **Secrets are sealed before they touch the DB** ([lib/secrets](lib/secrets))
  and are decrypted only inside the service — never returned to a caller.

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
│   ├── api/                 # gen.go (generated) + handlers.go + per-resource handler files (clusters.go, …)
│   ├── auth/                # RequireAuth + OIDC verifier (oidc.go) + dev passthrough + OrgContext (org.go)
│   ├── cache/               # Redis client
│   ├── clusters/            # cluster-registration domain service (worked-example resource)
│   ├── config/              # env loading
│   ├── db/                  # Postgres + ent wiring
│   ├── k8s/                 # Kubernetes connectivity probing (in-cluster, kubeconfig, token, eks/gke/aks)
│   ├── migrate/             # SQL migration runner
│   ├── organizations/       # organizations + memberships (tenancy)
│   ├── queue/               # River wrapper (worker registry, migrations)
│   ├── secrets/             # envelope encryption for credentials at rest (the Sealer)
│   ├── server/              # http.Server, request logging, route mounting
│   ├── testsupport/         # integration-test harness (isolated Postgres per test)
│   └── users/               # user provisioning (EnsureUser from the OIDC subject)
├── ui/
│   ├── embed.go             # //go:embed all:dist
│   ├── e2e/                 # Playwright browser tests (auth journey)
│   ├── playwright.config.ts # e2e config (starts/reuses API + Vite dev server)
│   ├── src/api/             # generated schema + openapi-fetch client
│   ├── src/components/      # auth/org gates (ApiAuthBinder, AuthGate, OrgGate), Layout, Sidebar (+ *.test.tsx)
│   ├── src/routes/          # page-level components (Home, AuthCallback, CreateOrganization, per-resource pages)
│   ├── src/test/            # Vitest setup
│   └── vite.config.ts       # dev server (:2424) /api + /config.js proxy; Vitest config
├── Makefile
├── docker-compose.yml       # Postgres + Redis + Dex for local dev
├── Dockerfile               # multi-stage → distroless single binary
├── TESTING.md               # testing strategy + how each layer works
└── .air.toml
```
