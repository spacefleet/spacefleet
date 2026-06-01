# Spacefleet

A Go backend + React SPA that ship as a single binary. The Go program serves
`/api/*` and the embedded Vite build from the same origin. A shared OpenAPI
spec drives both the server stubs and the typed TypeScript client.

This is a clean starting point: Go + Postgres (via [ent](https://entgo.io/)) +
Redis + a React/Vite/Tailwind SPA, with an OpenAPI-driven contract and an
example `notes` resource wired end-to-end. Authentication runs on
[Dex](https://dexidp.io/) (OIDC), bootstrapped for local dev in Docker Compose.

## Stack

- **Backend**: Go, `net/http`, ent ORM over Postgres, [River](https://riverqueue.com/) for background jobs, Redis cache.
- **Contract**: [`api/openapi.yaml`](api/openapi.yaml) → Go stubs (`oapi-codegen`) + TS types (`openapi-typescript`).
- **Frontend**: Vite + React 18 + TypeScript, React Router v7, Tailwind v4, `openapi-fetch`.
- **Processes**: `serve` (stateless HTTP API) and `worker` (River jobs). Default subcommand is `serve`.

## Prerequisites

- Go 1.25+ (uses the `tool` directive)
- Node 20+ and npm
- Docker (for local Postgres + Redis)
- [Air](https://github.com/air-verse/air) for `make dev` hot reload — `go install github.com/air-verse/air@latest`

## Running locally

**1. One-time setup**

```sh
cp .env.example .env   # sensible defaults; points at the dev Dex below
make secret-key        # generate a SPACEFLEET_SECRET_KEY, paste it into .env
make ui-install        # npm install inside ui/
```

`SPACEFLEET_SECRET_KEY` is the key used to envelope-encrypt stored credentials
(e.g. registered cluster tokens/kubeconfigs). `.env.example` ships a sample key
so the app runs immediately, but generate your own with `make secret-key` and
**never reuse the sample outside local dev**. Leaving it empty disables secret
storage — features that store no secrets (like in-cluster cluster registration)
still work.

(Plus the prerequisites above — notably `go install github.com/air-verse/air@latest`
for `make dev` hot reload.)

**2. Start the dependencies** (Postgres + Redis + Dex, in Docker)

```sh
make services-up
make migrate-up        # apply db/migrations against DATABASE_URL
```

`make migrate-up` is one-time per fresh database; re-run `make services-up`
after a reboot to bring the containers back.

**3. Run the app** — backend and Vite dev server, in two terminals:

```sh
make dev       # Go backend on :8080 (live reload)
make ui-dev    # Vite on :2424, proxies /api/* and /config.js to :8080
```

**4. Open <http://localhost:2424>.** You'll be redirected to Dex to sign in —
the seeded dev login is **`admin@example.com` / `password`**.

Vite proxies `/api/*` to the Go server, so the React code calls same-origin
paths — no CORS. In production the single binary serves both the embedded SPA
and `/api/*`.

The `worker` process is optional until you register background jobs:

```sh
make worker
```

> **Auth.** Dex is always Spacefleet's identity provider — there's no external
> or passthrough mode. The SPA logs in against Dex (Authorization Code + PKCE)
> and sends the ID token to the API, which verifies it (`lib/auth/oidc.go`) and
> **fails closed** — the server won't boot without `OIDC_ISSUER`, and tests
> inject a fake verifier (`lib/testsupport`). The app reverse-proxies Dex
> same-origin under `/dex` (`DEX_UPSTREAM_URL`), so the browser only ever talks
> to the app. The dev Dex is configured in
> [`dev/dex/config.yaml`](dev/dex/config.yaml) — in-memory, single static user,
> **dev only**; its issuer is the app origin (`http://localhost:2424/dex`).
> Enterprise SSO is wired through Dex's connectors, not by repointing the app.

## Editing the API

1. Edit [`api/openapi.yaml`](api/openapi.yaml).
2. `make gen` — regenerates the ent client, `lib/api/gen.go`, and `ui/src/api/schema.d.ts`.
3. Implement new methods on `api.Server` in [`lib/api/handlers.go`](lib/api/handlers.go) (the build breaks until you do — that's the gate).
4. Call it from the UI via the typed client:

   ```ts
   import { api } from "./api/client";
   const { data, error } = await api.GET("/api/notes");
   ```

## Editing the database schema

1. Add/modify a schema in [`ent/schema/`](ent/schema).
2. `make gen` to regenerate the ent client.
3. Add a matching SQL migration in [`db/migrations/`](db/migrations) and run `make migrate-up`.

## Testing

```sh
make test                    # Go unit tests (fast, no deps)
make test-integration        # Go integration tests vs real Postgres (needs services-up)
cd ui && npm test            # Frontend unit tests (Vitest + RTL)
make e2e                     # Browser e2e (Playwright) — needs the full stack up
make vet                     # go vet ./...
cd ui && npm run typecheck   # TS typecheck
```

See [TESTING.md](TESTING.md) for the strategy and how each layer works.

## Deploying

On every `v*` git tag, CI publishes two artifacts to GHCR, gated behind the
full lint/test matrix:

- the multi-arch container image — `ghcr.io/spacefleet/app:X.Y.Z`
- the Helm chart (OCI) — `oci://ghcr.io/spacefleet/charts/spacefleet`, version `X.Y.Z`

The Helm chart is the recommended way to deploy to Kubernetes. It runs the
`serve` and `worker` processes, applies migrations as a release hook, and always
bundles Dex (the identity provider); it also bundles Postgres + Redis by default
for a one-command trial (toggle those off for managed/external services):

```sh
# One-command trial (bundled Postgres + Redis + Dex; reach it via port-forward):
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z

# Production: give Dex a real hostname via ingress (its issuer becomes
# https://<host>/dex), then change the seeded admin / add connectors:
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

See [`deploy/charts/spacefleet/README.md`](deploy/charts/spacefleet/README.md)
for production configuration (external datastores, ingress, autoscaling, Dex
connectors). Lint and render the chart locally with `make helm-lint` /
`make helm-template`. The chart now has one subchart dependency (`dexidp/dex`),
so `make helm-*` run `helm dependency build` for you.

## The example `notes` resource

`Note` (ent schema, migration, `/api/notes` endpoints, and the `Home` page)
exists only to demonstrate the full data path. Delete it once you have real
resources.

## License

Spacefleet is open source under the [Apache License 2.0](LICENSE).
Contributions are welcome — by submitting a contribution you agree to license
it under the same terms.
