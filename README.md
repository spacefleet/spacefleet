# Spacefleet

Spacefleet is a self-hostable deployment platform for Kubernetes and cloud
infrastructure. An **application** is a **workflow**: a graph of typed
**components** — Helm charts, raw manifests, and OpenTofu (Terraform) root
modules — that deploy in dependency order on a **runner cluster** you
register. A **run** of that workflow (deploy, preview, uninstall, drift
check) executes each component as a job on the runner, with approval gates,
plan review, policy checks, drift detection, and a live log stream along the
way. It is multi-tenant (users belong to organizations) and ships as a single
Go binary that serves both the API and the embedded React SPA.

What it does today:

- **Workflows** of Helm, manifest, and OpenTofu components arranged in
  stages, with `${{ }}` interpolation between steps and a stage-by-stage run
  view.
- **OpenTofu, first class** — plan review with per-resource diffs, gated
  applies, managed S3/GCS/Azure state backends, workspaces, `TF_VAR` inputs,
  a provider plugin cache, captured outputs and resource inventory, drift
  detection (on demand and scheduled), guarded state operations
  (`force-unlock`, `state rm`/`mv`, `import`), per-component destroy and
  targeted runs.
- **Governance** — approval policies (named approvers, N-of-M, no
  self-approval, timeouts), Rego plan policies that block or warn before an
  apply, and notifications to email, Slack, or any webhook.
- **GitHub** — private repositories through a GitHub App, push and
  pull-request triggers, and plan results posted back as checks.
- **Platform** — Kubernetes cluster registration (token, kubeconfig, EKS, GKE,
  AKS, in-cluster), Tekton as the job runner, cloud credentials sealed at
  rest, Dex (OIDC) authentication with SSO through connectors.

The user and operator guides live in [`docs/`](docs/); the contributor guide
is [CLAUDE.md](CLAUDE.md).

## Stack

- **Backend**: Go, `net/http`, ent ORM over Postgres, [River](https://riverqueue.com/) for background jobs, [OPA](https://www.openpolicyagent.org/) for plan policies.
- **Runner**: [Tekton](https://tekton.dev/) on a registered Kubernetes cluster runs every component job (Helm, `kubectl`, `tofu`).
- **Contract**: [`api/openapi.yaml`](api/openapi.yaml) → Go stubs (`oapi-codegen`) + TS types (`openapi-typescript`).
- **Frontend**: Vite + React 18 + TypeScript, React Router v7, Tailwind v4, React Flow for the workflow builder, `openapi-fetch`.
- **Processes**: `serve` (stateless HTTP API) and `worker` (workflow runs, scheduled drift checks, notifications, Tekton installs, emails). Default subcommand is `serve`.

## Prerequisites

- Go 1.25+ (uses the `tool` directive)
- Node 20+ and npm
- Docker (for local Postgres + Dex)
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

**2. Start the dependencies** (Postgres + Dex, in Docker)

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

**5. Run the worker** — it executes workflow runs, so nothing deploys
without it:

```sh
make worker
```

To run a workflow you also need a **runner cluster**: register a Kubernetes
cluster under Admin → Clusters and set it up as a runner (Spacefleet installs
Tekton on it) — see [Runner clusters](docs/user/running-jobs.md). For local development,
a kind cluster is the simplest option (step 6).

**6. (Optional) A local cluster with kind** — [kind](https://kind.sigs.k8s.io/)
runs Kubernetes inside Docker, which you already have for step 2:

```sh
brew install kind      # or see the kind docs for other platforms
kind create cluster --name spacefleet
kind get kubeconfig --name spacefleet | pbcopy    # macOS; otherwise redirect to a file
```

Under **Admin → Clusters**, register it with the **Kubeconfig** connection
method and paste that kubeconfig, then click **Set up as runner** and confirm to
install Tekton. A few things to know:

- **Paste the output of `kind get kubeconfig`, not your whole `~/.kube/config`.**
  Spacefleet rejects any kubeconfig whose users authenticate through an `exec`
  or `auth-provider` plugin (e.g. `aws eks get-token`, `gke-gcloud-auth-plugin`)
  — it checks *every* user in the file, not just the current context. kind's
  kubeconfig uses a static client certificate, so it's accepted.
- **`ALLOW_PRIVATE_CLUSTER_ENDPOINTS=true` must be set** in `.env` (it is in
  `.env.example`). kind's API server is at `https://127.0.0.1:<port>`, and
  loopback/private endpoints are refused without it.
- **Use the one kind cluster as both the runner and the deploy target.** When
  runner and target are the same cluster, jobs reach its API server at
  `kubernetes.default.svc` instead of the host-side `127.0.0.1` address. Two
  separate kind clusters won't work as runner + target out of the box: a pod
  in one can't reach the other's `127.0.0.1` port.
- **OpenTofu components on managed state call back to Spacefleet** from the
  runner pod. `RUNNER_API_URL=http://host.docker.internal:8080` in `.env`
  (it is in `.env.example`) points them at the Go backend on your machine —
  not the Vite origin. That name resolves inside kind on Docker Desktop; on
  Linux use the kind network's gateway IP instead
  (`docker network inspect kind -f '{{(index .IPAM.Config 0).Gateway}}'`).
- **`kind create cluster` switches kubectl's current context** to
  `kind-spacefleet`. Check `kubectl config current-context` before running
  anything against a real cluster afterwards.

Reset with `kind delete cluster --name spacefleet` and create it again.

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
3. Implement new methods on `api.Server` in [`lib/api/`](lib/api) — cross-cutting handlers in `handlers.go`, a file per resource otherwise (the build breaks until you do — that's the gate).
4. Call it from the UI via the typed client:

   ```ts
   import { api } from "./api/client";
   const { data, error } = await api.GET("/api/clusters");
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

- the multi-arch container image — `ghcr.io/spacefleet/spacefleet:X.Y.Z`
- the Helm chart (OCI) — `oci://ghcr.io/spacefleet/charts/spacefleet`, version `X.Y.Z`

The Helm chart is the recommended way to deploy to Kubernetes. It runs the
`serve` and `worker` processes, applies migrations as a release hook, and always
bundles Dex (the identity provider); it also bundles Postgres by default
for a one-command trial (toggle that off for a managed/external database):

```sh
# One-command trial (bundled Postgres + Dex; reach it via port-forward):
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z

# Production: give Dex a real hostname via ingress (its issuer becomes
# https://<host>/dex), then change the seeded admin / add connectors:
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

See [`deploy/charts/spacefleet/README.md`](deploy/charts/spacefleet/README.md)
for production configuration (external database, ingress, autoscaling, Dex
connectors). Lint and render the chart locally with `make helm-lint` /
`make helm-template`. The chart now has one subchart dependency (`dexidp/dex`),
so `make helm-*` run `helm dependency build` for you.

## Documentation

- **Users** — [deploy workflows](docs/user/deploy-workflows.md) (components,
  OpenTofu, runs, approvals, triggers), [runner clusters](docs/user/running-jobs.md),
  [variable interpolation](docs/user/variable-interpolation.md),
  [importing Helm releases](docs/user/importing-helm-releases.md),
  [plan policies](docs/user/policies.md), [notifications](docs/user/notifications.md).
- **Operators** — [install with Helm](docs/operator/install-with-helm.md),
  [authentication](docs/operator/authentication.md), [database](docs/operator/database.md),
  [secrets](docs/operator/secrets.md), [email](docs/operator/email.md),
  [private Git repositories and the GitHub App](docs/operator/private-git-charts.md).
- **Contributors** — [CLAUDE.md](CLAUDE.md) (architecture, conventions, how a
  resource is built) and [TESTING.md](TESTING.md).

## License

Spacefleet is open source under the [Apache License 2.0](LICENSE).
Contributions are welcome — by submitting a contribution you agree to license
it under the same terms.
