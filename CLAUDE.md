# CLAUDE.md

Spacefleet is a self-hostable deployment platform: a Go backend + React SPA
that ship as a single binary. The Go program serves `/api/*` and the embedded
Vite build from the same origin in production. A shared OpenAPI spec drives
both the server stubs and the TypeScript client.

The stack: Go + Postgres (ent) + River (jobs) + OPA (plan policies) + a
React/Vite/Tailwind SPA, with an OpenAPI-driven contract, Dex (OIDC)
authentication, and Tekton on a registered Kubernetes cluster as the job
runner. The domain is multi-tenant — users belong to **organizations** (via
memberships) and every resource is scoped to an org. The product domain is
**applications → stages → components → workflow runs** (see [The domain](#the-domain)
below); **Kubernetes cluster registration** is the simplest org-scoped
resource and remains the worked example for how a resource is built end to
end (see [How a resource is built](#how-a-resource-is-built)). Tests span Go
unit/integration, frontend unit (Vitest), and browser e2e (Playwright) — see
[TESTING.md](TESTING.md).

**This is an open-source project** (Apache-2.0, see [LICENSE](LICENSE)). The
entire platform is open source, and it may be self-hosted by anyone, so nothing
in here may assume or hardcode "we are the ones running it" — keep
operator-specific assumptions out of the codebase.

If a CLAUDE.custom.md file exists in the root of the project, read that first
and then continue with the rest of this document.

## The domain

- **Application** ([ent/schema/application.go](ent/schema/application.go),
  [lib/applications](lib/applications)) — the owner of a deploy workflow. It
  carries only a name, a **runner cluster** (a Tekton-enabled registered
  cluster where its jobs execute), an optional group (folder), and run
  settings (drift schedule, push/PR triggers). Targeting lives on components.
- **Stage** ([ent/schema/workflowstage.go](ent/schema/workflowstage.go),
  [lib/workflows/stages.go](lib/workflows/stages.go)) — a workflow is an
  ordered list of named stages (`ordinal`); a stage holds components that run
  in parallel, and the next stage starts once every component of the previous
  one has finished. Stage order is the **only** ordering — there are no
  authored per-component dependencies, groups, or canvas positions. The
  scheduler never sees a stage: at snapshot time `stageDependencies` desugars
  stage order into step-level `depends_on` (each component waits on every
  component of the previous non-empty stage). The workflow is saved whole
  (`PUT …/workflow` with `stages[]`), and ids are client-provided so a
  component's variables/state/history survive the delete-and-recreate.
- **Component** ([ent/schema/component.go](ent/schema/component.go),
  validated in [lib/workflows/dag.go](lib/workflows/dag.go)) — one typed step
  of the workflow, in one stage (`stage_id`, display `ordinal`): `helm` (a
  chart from an HTTP repo, OCI, or git), `manifest` (git-sourced Kubernetes
  manifests), or `terraform` (an OpenTofu root module with a managed
  s3/gcs/azurerm backend). A flat `config` string map holds the type-specific
  settings; approval flags and an approval policy gate the step. A
  `${{ components.<name>.outputs.* }}` reference must name an OpenTofu
  component in an earlier stage.
- **Workflow run** ([ent/schema/workflow_run.go](ent/schema/workflow_run.go),
  [lib/workflows/runs.go](lib/workflows/runs.go)) — one execution of the
  workflow with an action: `deploy`, `uninstall`, `preview` (dry run), `drift`
  (refresh-only plans), or `state_op` (a guarded OpenTofu state operation).
  A run **snapshots the graph** at start (`graph`: execution nodes with their
  desugared `depends_on`, plus the stages they ran in), so edits never change
  an in-flight run; per-run arguments live in `args` (a state op, or a
  component-scoped `RunScope` with `-target`s) and `trigger` (the GitHub
  event that started it). One **component run** per execution unit records
  status, logs, the parsed plan, captured outputs/resources, approvals, and
  the policy verdict. An OpenTofu component expands into a **plan unit** and
  an **apply unit** (`expandExecutionNodes`); the planfile crosses between
  them through a per-run Kubernetes Secret. Run responses carry a server-built
  stage summary (`RunStages` in
  [lib/workflows/runstages.go](lib/workflows/runstages.go)): steps folded
  into components, components into stages, statuses combined. Runs from
  before stages existed have no recorded stages, so their stages are derived
  from the snapshot's `depends_on` by longest-path layering.
- **The worker** ([lib/workflows/worker.go](lib/workflows/worker.go)) drives a
  run: it plans each unit through [lib/deploy](lib/deploy) (resolving
  cluster connections, credentials, GitHub tokens, cloud auth, variables),
  submits it as a Tekton TaskRun on the runner via [lib/tekton](lib/tekton),
  watches it to terminal, captures logs, and settles status through the pure
  scheduler in [scheduler.go](lib/workflows/scheduler.go). Gates park the run
  at `awaiting_approval`; the approve/reject handler enqueues a resume. The
  OpenTofu plan gate ([policygate.go](lib/workflows/policygate.go)) evaluates
  Rego policies ([lib/policy](lib/policy)) after a plan unit succeeds. Every
  settle path emits run events ([events.go](lib/workflows/events.go)) that
  [lib/notifications](lib/notifications) fans out.
- **Scripts** — each component type renders a `/bin/sh` script the TaskRun
  runs: [lib/helm](lib/helm), [lib/manifest](lib/manifest), and
  [lib/tofu](lib/tofu) (plan/apply/destroy, drift, state ops, backend
  override, workspace, the planfile handover). Secrets never appear in a
  script: they are mounted as files from a per-run Secret.
- **Managed state** ([lib/tofustate](lib/tofustate)) — the `spacefleet`
  OpenTofu backend (the default for new components): Spacefleet serves
  OpenTofu's `http` backend itself and keeps state in `tofu_states` /
  `tofu_state_versions` (gzipped, sealed, every version kept). The planner
  mints a per-step HMAC token (read scope for plan/preview/drift units, write
  for apply/destroy/state-op units) delivered as `TF_HTTP_PASSWORD` from the
  creds Secret, and points the backend at `RUNNER_API_URL` (or
  `IN_CLUSTER_API_URL` for an in_cluster runner). Every request re-checks that
  the step is still running. A workflow save refuses to switch the backend of
  a component whose recorded state still lists resources unless
  `allow_backend_change` is set.

Cross-cutting features layered on that: variables and interpolation
([lib/variables](lib/variables), [lib/interpolate](lib/interpolate)), GitHub
App installations and webhooks ([lib/githubapp](lib/githubapp),
[lib/githubinstallations](lib/githubinstallations),
[lib/workflows/triggers.go](lib/workflows/triggers.go)), cloud and chart
credentials sealed by [lib/secrets](lib/secrets), and the Helm-release import
flow ([lib/helm/rollout.go](lib/helm/rollout.go), [lib/applications](lib/applications)).

## Architecture essentials

- **Entrypoint**: [cmd/spacefleet/main.go](cmd/spacefleet/main.go) dispatches by subcommand — `serve` (HTTP API, the default), `worker` (River background jobs), `migrate` (SQL migrations).
- **HTTP server**: built in [lib/server/server.go](lib/server/server.go) (wires Postgres+ent, the credential sealer ([lib/secrets](lib/secrets)), and the domain services), routed in [lib/server/routes.go](lib/server/routes.go).
- **Routing** mounts three things on one `*http.ServeMux`:
  1. Generated `/api/*` handlers behind the `RequireAuth` middleware.
  2. `/config.js` — emits `window.appConfig` with non-secret OIDC values.
  3. `/` → [ui/embed.go](ui/embed.go), the embedded SPA, with `index.html` fallback for client-side routing.
- **Public routes**: `/api/health`, the GitHub webhook (`POST /api/webhooks/github`, authenticated by its HMAC signature), and the managed OpenTofu state routes (`GET|POST /api/tofu/state/{componentId}/{workspace}`, `POST|DELETE …/lock` — OpenTofu's `http` backend, called by runner pods with a per-step state token as the basic-auth password; see [lib/api/tofustate.go](lib/api/tofustate.go)) bypass the auth chain; everything else under `/api/*` requires a Dex ID token.
- **Auth**: **Dex (OIDC), always bundled** — Spacefleet has no external-provider or passthrough mode; Dex is treated as an internal part of the platform, and SSO is done by configuring Dex's *connectors* (GitHub/Google/Okta/Entra/LDAP/SAML), not by pointing the app elsewhere. It sits behind a seam in [lib/auth](lib/auth): `RequireAuth` takes a `TokenVerifier`, and [server.go](lib/server/server.go) builds the OIDC verifier ([lib/auth/oidc.go](lib/auth/oidc.go)) that validates Dex-issued **ID tokens** (signature via JWKS, `iss`/`exp`/`aud`). It **fails closed** — `buildVerifier` errors (boot fails) when `OIDC_ISSUER` is unset, and `RequireAuth` rejects every protected request if handed a nil verifier; there is no allow-everyone fallback. Tests inject a fake verifier ([lib/testsupport](lib/testsupport)). `publicAPIPaths` lists the bypass paths (`/api/health`). The app **reverse-proxies Dex same-origin under `/dex`** (`DEX_UPSTREAM_URL`, see [routes.go](lib/server/routes.go)), so the browser only ever talks to the app — Dex is never exposed directly. In dev, Dex runs in Docker Compose, bootstrapped from [dev/dex/config.yaml](dev/dex/config.yaml). Operator-facing setup instructions (not code internals) live in [docs/operator/authentication.md](docs/operator/authentication.md) — see [End-user docs](#end-user-docs-docs).
- **Tenancy**: a second middleware, `OrgContext` ([lib/auth/org.go](lib/auth/org.go)), lifts the SPA's `X-Organization-ID` header onto the request context. It does **no** authorization — org-scoped handlers resolve the org and check the caller's membership themselves (`Server.currentOrg`). Auth runs outermost, then org resolution.
- **Frontend**: Vite + React 18 + TS, React Router v7, Tailwind v4, a stage-column workflow builder and a stage/step-rail run view ([ui/src/components/workflow](ui/src/components/workflow)). Pages are generated from the nav config ([ui/src/nav.ts](ui/src/nav.ts)) and mapped to components in [App.tsx](ui/src/App.tsx). Live data (run status, logs, cluster resources) arrives over Server-Sent Events ([lib/api/stream.go](lib/api/stream.go), [ui/src/lib/useObjectStream.ts](ui/src/lib/useObjectStream.ts)). The typed API client is in [ui/src/api/client.ts](ui/src/api/client.ts). Login uses `react-oidc-context` (Authorization Code + PKCE, public client): `AuthProvider` is configured in [main.tsx](ui/src/main.tsx) from `window.appConfig`, `AuthGate` redirects unauthenticated users to Dex, and `ApiAuthBinder` feeds the ID token to the API client as the bearer token.

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
`spacefleet worker` runs the River background-job worker plus a few loops.
Both read the same `.env`; both build the domain services (the worker needs
the sealer, the GitHub App, and the resolver to open credentials at run time
— job args carry only ids, never secrets). The registry, wired in
[cmd/spacefleet/worker.go](cmd/spacefleet/worker.go):

| Job kind | Worker | Enqueued by |
| --- | --- | --- |
| `workflow_run` | `workflows.WorkflowRunWorker` — executes a run's step DAG (stage order, desugared) | the API (start run / approve), the webhook, the drift scheduler |
| `notification_deliver` | `notifications.DeliverWorker` — one event to one channel | `notifications.Service.Dispatch` (worker) and the test-send endpoint |
| `tekton-install` | `tekton.InstallWorker` — installs/uninstalls Tekton on a cluster | the API |
| `invite_email` | `email.InviteEmailWorker` | the API |

Loops in the worker process: the **reaper** (settles runs whose worker died),
the **approval-timeout sweep**, and the **drift scheduler** (starts scheduled
`drift` runs). Enqueueing from inside the worker goes through injected
`EnqueueRunFunc`/`EnqueueFunc` seams so `lib/workflows` and
`lib/notifications` never import River's client.

## Deployment (Helm + GHCR)

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) publishes two
artifacts on every `v*` tag, behind the full lint/test gate: the multi-arch
image (`ghcr.io/spacefleet/spacefleet:X.Y.Z`) and the Helm chart as an OCI artifact
(`oci://ghcr.io/spacefleet/charts/spacefleet`, version `X.Y.Z`). The chart's
`version`/`appVersion` are stamped from the tag at package time — the committed
`Chart.yaml` carries `0.0.0` placeholders.

The chart ([deploy/charts/spacefleet](deploy/charts/spacefleet)) deploys `serve`
(web) + `worker`, runs `migrate up` as a `post-install,pre-upgrade` hook Job
(post-install, not pre-, so it can reach the bundled Postgres), and builds
`DATABASE_URL` into a Secret. Postgres is bundled as a small
**first-party StatefulSet running the official upstream image** (the same
`postgres:18-alpine` as docker-compose). On by default for
one-command trials; disable + use `externalDatabase` for prod.

**Auth is always bundled Dex — one mode, no toggle.** The chart **always
bundles Dex** via the official `dexidp/dex` **subchart** (there is no
`dex.enabled` and no external-provider option): it renders Dex's config itself
(so the app's PKCE client + derived redirect URIs, storage, and connectors are a
single source of truth) and hands it to the subchart as an existing Secret
(`dex.configSecret.create=false`, name `dex.configSecret.name`, key
`config.yaml`). The app **reverse-proxies Dex same-origin under `/dex`**
(`DEX_UPSTREAM_URL` → the in-cluster Dex Service), so the ingress backs only the
app and Dex's Service stays internal — the browser never talks to Dex directly.
The issuer is therefore always the app's own origin + `/dex`: derived
`https://<ingress host>/dex`, or `http://localhost:8080/dex` for a port-forward
trial when ingress is off. Storage defaults to `crd` (Kubernetes CRDs, no DB).
The backend verifies tokens against the in-cluster Dex Service via
`OIDC_JWKS_URL` (see [lib/auth/oidc.go](lib/auth/oidc.go)) so it never depends on
the public issuer being reachable in-cluster. Enterprise SSO is configured via
`dex.connectors`, not by repointing the app.

The dex subchart is this chart's one third-party dependency — pinned in
`Chart.lock`, vendored under `charts/`. When changing chart templates, run
`make helm-lint` (it runs `helm dependency build` first, via `helm-deps`); the
`lint-helm` CI job gates the same way and renders the bundled-Dex value set
(`ci/dex-values.yaml`) too.

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
make services-up   # Postgres + Dex (OIDC) — Dex container on :5556
make migrate-up    # apply migrations
make dev           # Go backend on :8080 (Air live-reload)
make ui-dev        # Vite on :2424, proxies /api/*, /config.js, /dex/* to :8080
```

Open <http://localhost:2424>. You'll be redirected to Dex to log in — the dev
login is **`admin@example.com` / `password`** (seeded in
[dev/dex/config.yaml](dev/dex/config.yaml)). There is **no way to skip auth** —
Dex is mandatory, and the backend refuses to boot without `OIDC_ISSUER`. So
`make dev` needs the Dex container up (`make services-up`).

Everything is **same-origin in dev** (`:2424`), matching prod. Dex is reached
under the app's `/dex` path: the browser hits `:2424/dex` → Vite proxies to the
Go backend (`:8080`) → the backend reverse-proxies to the Dex container
(`:5556`). So the dev issuer is `http://localhost:2424/dex`, there are **no
cross-origin calls and no `allowedOrigins` to maintain**, and the backend
fetches signing keys directly from Dex via `OIDC_JWKS_URL` (mirroring the
in-cluster Helm setup). The app↔API is same-origin too (Vite proxy in dev,
embedded binary in prod).

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

## Definition of done: CI must pass

**No change is "done" until the CI jobs that gate it pass.** Don't rely on "it
builds" or "the test I ran is green" — reproduce the *actual* CI checks locally
and make them pass before you call work complete or hand it back. CI is defined
in [.github/workflows/ci.yml](.github/workflows/ci.yml); run the jobs that cover
what you touched:

| CI job | What it runs (reproduce locally) |
| --- | --- |
| `lint-go` | `gofmt -l .` (must be empty); `go mod tidy` then **no** `git diff` in `go.mod`/`go.sum`; `golangci-lint run ./...` (pinned **v2.11.3**, config `.golangci.yml`) |
| `test-go` | `make vet` (`go vet ./...`) and `make test` (`go test ./...`) |
| `test-integration` | `make test-integration` (`go test -tags=integration ./...`, needs `make services-up`) |
| `lint-ui` | `cd ui && npm run lint` |
| `test-ui` | `cd ui && npm run typecheck && npm test && npm run build` |
| `lint-helm` | `make helm-lint` (renders the default, external, and dex value sets) |
| `e2e` | `make e2e` (Playwright; brings up services + migrations) |

Notes that bite:

- **`go mod tidy` must be a no-op.** Adding a dependency — including one only a
  `_test.go` imports — changes `go.mod`; if you don't commit the tidy result,
  `lint-go` fails on the `git diff --exit-code` even when code compiles. Run
  `go mod tidy` and commit `go.mod`/`go.sum` as part of the change.
- **golangci-lint version skew.** The pinned linter refuses to run if the
  `golangci-lint` binary was built with an older Go than this module's `go`
  directive ("language version … is lower than the targeted version"). Match it
  with `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3`.
- **Seed `ui/dist` first.** Go lint/build need a file under `ui/dist` for
  `//go:embed all:dist` — run `make ui-build` (or the `.gitkeep`/`index.html`
  seed CI uses) if you wiped it. See the gotcha below.
- `docker-app` / `helm-chart` publish only on `v*` tags and aren't part of the
  per-PR gate; the lint/test jobs above are.

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
  `NewOIDCVerifier`, …), packages, function/middleware names, or describe request
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
- **Dex reads its config only at startup.** After editing [dev/dex/config.yaml](dev/dex/config.yaml), `make services-up` won't pick it up (the service definition is unchanged) — run `docker compose restart dex`. (That file is **dev only**; the Helm chart's bundled Dex config is rendered from `dex.*` values into a Secret by [templates/dex-config.yaml](deploy/charts/spacefleet/templates/dex-config.yaml) — change the values and `helm upgrade`, never hand-edit the rendered Secret.)
- **The UI dev port (`2424`) is the dev origin, pinned in three places.** [vite.config.ts](ui/vite.config.ts) (`strictPort`), Dex's `issuer` + `redirectURIs` in [dev/dex/config.yaml](dev/dex/config.yaml), and `OIDC_ISSUER` in `.env` (`http://localhost:2424/dex`). Changing the port means updating all three, then restarting Dex. (Login is same-origin now, so hitting the backend directly on `:8080` is **not** a supported login origin in dev — open `:2424`.)
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
spacefleet/
├── api/openapi.yaml         # shared contract (drives Go + TS)
├── cmd/spacefleet/          # main.go (subcommand dispatch) + serve.go + worker.go + migrate.go
├── db/migrations/           # hand-written SQL migrations
├── deploy/charts/spacefleet # Helm chart (serve+worker+migrate, bundled Dex, optional bundled PG) — published to GHCR as OCI on v* tags
├── dev/dex/config.yaml      # Dex (OIDC) bootstrap for local dev — static client + dev login
├── docs/                    # end-user docs: operator/ (deploying) and user/ (using the app)
├── ent/                     # ent ORM: schema/ (hand-written) + generated client
├── lib/
│   ├── api/                 # gen.go (generated) + handlers.go + a handler file per resource + SSE streams + the GitHub webhook
│   ├── applicationgroups/   # application folders
│   ├── applications/        # applications (workflow owners) + the Helm-release import flow
│   ├── auth/                # RequireAuth (fails closed) + OIDC verifier (oidc.go) + OrgContext (org.go)
│   ├── chartcredentials/    # private Helm registry/repo credentials (sealed)
│   ├── cloudauth/           # per-provider env for cloud credentials (AWS/GCP/Azure) in jobs
│   ├── cloudcredentials/    # cloud-provider credential sets (sealed)
│   ├── clusters/            # cluster registration + Tekton installation state + plugin cache
│   ├── config/              # env loading
│   ├── db/                  # Postgres + ent wiring
│   ├── deploy/              # the run-input resolver (connections, credentials, git tokens, cloud auth, variables)
│   ├── email/               # SMTP sender + invitation email job
│   ├── githubapp/           # GitHub App auth, installation tokens, webhooks, check runs
│   ├── githubinstallations/ # an org's GitHub App installations
│   ├── helm/                # Helm rollout script rendering + revision parsing
│   ├── interpolate/         # the ${{ }} template parser
│   ├── invitations/         # org invitations
│   ├── k8s/                 # Kubernetes connectivity (in-cluster, kubeconfig, token, eks/gke/aks), capabilities, resource reads
│   ├── manifest/            # kubectl apply/diff script rendering
│   ├── migrate/             # SQL migration runner
│   ├── notifications/       # notification channels, event dispatch, delivery job
│   ├── organizations/       # organizations + memberships (tenancy)
│   ├── policies/            # plan policies (CRUD over lib/policy)
│   ├── policy/              # the Rego/OPA plan-policy engine (pure)
│   ├── queue/               # River wrapper (worker registry, migrations, client)
│   ├── secrets/             # envelope encryption for credentials at rest (the Sealer)
│   ├── server/              # http.Server, request logging, route mounting, service wiring
│   ├── slug/                # DNS-label name validation
│   ├── tekton/              # Tekton install, TaskRun submit/watch, handover Secrets, plugin cache
│   ├── testsupport/         # integration-test harness (isolated Postgres per test) + fake verifier
│   ├── tofu/                # OpenTofu script rendering, plan parsing, state ops, targets, versions
│   ├── tofustate/           # managed OpenTofu state: the http backend's server side (state tokens, locks, sealed versions)
│   ├── users/               # user provisioning (EnsureUser from the OIDC subject)
│   ├── variables/           # org/group/app/component variables (sensitive ones sealed) + env resolution
│   └── workflows/           # the workflow domain: stages + validation, runs (snapshot, stage summary), expansion, planner, worker, scheduler, approvals, drift, state ops, scoped runs, triggers, events, policy gate, reaper
├── ui/
│   ├── embed.go             # //go:embed all:dist
│   ├── e2e/                 # Playwright browser tests
│   ├── playwright.config.ts # e2e config (starts/reuses API + Vite dev server)
│   ├── src/api/             # generated schema + openapi-fetch client
│   ├── src/components/      # auth/org gates, Layout, Sidebar, panels, workflow/ (stage columns, stage bar, plan views, state panel)
│   ├── src/contexts/        # OrgContext (current org + role), WorkflowDraftContext
│   ├── src/lib/             # appConfig, SSE hooks, formatting helpers
│   ├── src/nav.ts           # the nav config routes are generated from
│   ├── src/routes/          # page-level components (applications, workflow builder, run views, clusters, admin pages)
│   ├── src/test/            # Vitest setup
│   └── vite.config.ts       # dev server (:2424) /api + /config.js + /dex proxy; Vitest config
├── Makefile
├── docker-compose.yml       # Postgres + Dex for local dev
├── Dockerfile               # multi-stage → distroless single binary
├── TESTING.md               # testing strategy + how each layer works
└── .air.toml
```
