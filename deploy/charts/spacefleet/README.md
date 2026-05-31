# Spacefleet Helm chart

Deploys [Spacefleet](https://github.com/spacefleet/app) — the Go + React
single-binary app — to Kubernetes: the `serve` web/API process, the `worker`
background-job process, and a `migrate up` release hook, plus optional bundled
Postgres, Redis, and Dex (OIDC).

The chart is published as an **OCI artifact to GHCR** by the same CI pipeline
that builds the image, and its `version`/`appVersion` track the app's `v*` git
tags — so `--version X.Y.Z` always pairs with image `:X.Y.Z`.

## Install

Out-of-the-box trial (bundled Postgres + Redis, dev passthrough auth):

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z
```

Bundled Dex (real OIDC out of the box, seeded admin login):

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  --set dex.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

This serves Dex same-origin at `https://spacefleet.example.com/dex` and seeds
`admin@example.com` / `password` — **change it** (see [Authentication](#authentication)).

Production-style (external datastores + your OIDC provider):

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  --set config.oidc.issuer=https://dex.example.com \
  --set postgresql.enabled=false \
  --set externalDatabase.existingSecret=spacefleet-db \
  --set redis.enabled=false \
  --set externalRedis.existingSecret=spacefleet-redis \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

`helm test spacefleet` runs a smoke check against `/api/health`.

## What it deploys

| Resource | When | Notes |
| --- | --- | --- |
| `Deployment/<rel>-web` | always | `serve`; HTTP `:8080`, `/api/health` probes |
| `Deployment/<rel>-worker` | `worker.enabled` | `worker`; River jobs |
| `Job/<rel>-migrate` | `migrations.enabled` | `migrate up` as a `post-install,pre-upgrade` hook |
| `Service/<rel>` | always | ClusterIP → web |
| `Ingress/<rel>` | `ingress.enabled` | |
| `HorizontalPodAutoscaler` | `web.autoscaling.enabled` | targets the web Deployment |
| `Secret/<rel>-env` | unless both URLs come from existing Secrets | holds `DATABASE_URL` / `REDIS_URL` |
| `StatefulSet/<rel>-postgresql` (+ Service, Secret) | `postgresql.enabled` | bundled Postgres, official `postgres` image |
| `StatefulSet/<rel>-redis` (+ Service, Secret) | `redis.enabled` | bundled Redis, official `redis` image |
| `Deployment/<rel>-dex` (+ Service, RBAC, SA) | `dex.enabled` | bundled Dex via the `dexidp/dex` subchart |
| `Secret/<rel-configured>-dex-config` | `dex.enabled` | Dex config this chart renders for the subchart |

The migrate Job runs `post-install` (not `pre-install`) so it can reach the
bundled Postgres, which is a normal release resource and therefore created
after pre-install hooks would run. On upgrades it runs `pre-upgrade`, before
the new web/worker code rolls out. `migrations.backoffLimit` covers the window
while Postgres becomes reachable.

The bundled datastores are **single-replica StatefulSets running the official
upstream images** (the same `postgres:18-alpine` / `redis:7-alpine` as the dev
`docker-compose.yml`). They're meant for trials and small deployments, not HA;
for production use a managed/HA datastore via `externalDatabase` /
`externalRedis`.

The chart has one chart dependency: the official `dexidp/dex` subchart, used
only when `dex.enabled=true` (see [Authentication](#authentication)). It is
pinned in `Chart.lock` and vendored under `charts/`, so `helm dependency build`
runs before lint/package (CI and `make helm-deps` handle this).

## Datastore configuration

The chart builds `DATABASE_URL` / `REDIS_URL` for you:

- **Bundled** (`postgresql.enabled` / `redis.enabled`, the default): the URL is
  built from the chart's auth values and written into `Secret/<rel>-env`.
  **Override `postgresql.auth.password` and `redis.auth.password` for any real
  deployment.**
- **External, inline URL** (`externalDatabase.url` / `externalRedis.url`): the
  URL you provide is written into `Secret/<rel>-env`.
- **External, existing Secret** (`externalDatabase.existingSecret` /
  `externalRedis.existingSecret`): the pods reference your Secret directly; the
  chart writes nothing. Preferred for production — keeps credentials out of
  Helm values/release history.

If a datastore is neither bundled nor externally configured, templating fails
fast with an explanatory error.

## Authentication

The app authenticates users against an OIDC provider. There are three modes:

1. **Bundled Dex** (`dex.enabled=true`). The chart deploys the official
   `dexidp/dex` subchart and renders its config for you. By default Dex is served
   **same-origin** at `https://<your ingress host>/dex` (no CORS), so it needs
   ingress enabled or `dex.issuer` set — the render fails fast otherwise. The
   backend verifies tokens against the in-cluster Dex Service (`OIDC_JWKS_URL`),
   so it never has to reach the public issuer URL from inside the cluster.
   - **Storage** defaults to `dex.storage=crd` (Dex stores signing keys/sessions
     as Kubernetes CRDs — durable across restarts and HA-safe, no database).
     `postgres`, `sqlite3`, and `memory` are also available; `memory` is
     single-replica/trial only.
   - **A seeded admin** (`admin@example.com` / `password`) makes a fresh install
     loginnable. **This is a publicly known credential** — before exposing the
     deployment, replace `dex.staticPasswords` (regenerate a hash with
     `htpasswd -bnBC 10 "" <pw> | tr -d ':\n'`) or configure a connector and
     clear it.
   - **Connectors** (GitHub, Google, OIDC, LDAP, …) go in `dex.connectors`,
     passed through verbatim. Keep client secrets out of values — reference them
     as `$VAR` (Dex expands env at load) and inject the env via `dex.envFrom`:

     ```sh
     kubectl create secret generic spacefleet-dex-connectors \
       --from-literal=GITHUB_CLIENT_SECRET=...
     ```
     ```yaml
     dex:
       enabled: true
       connectors:
         - type: github
           id: github
           name: GitHub
           config:
             clientID: <oauth app id>
             clientSecret: $GITHUB_CLIENT_SECRET
             orgs: [{name: your-org}]
       envFrom:
         - secretRef: {name: spacefleet-dex-connectors}
     ```

2. **External provider** (`config.oidc.issuer=https://…`). Point at your own
   Dex/Keycloak/Auth0/Okta. This takes precedence over bundled Dex. Register the
   app as a public (PKCE) client with redirect URI
   `https://<your host>/auth/callback`.

3. **Dev passthrough** (neither of the above). The backend authenticates every
   request as `dev-user` — fine for a local port-forward trial, **never expose
   it**. This is the default when `dex.enabled=false` and `config.oidc.issuer` is
   empty.

`config.oidc.issuer` / `config.oidc.clientID` are non-secret and are surfaced to
the browser via `/config.js`.

## PodSecurity note

The bundled Postgres/Redis pods run the official images, whose entrypoints start
as root and drop to their service user — fine under **baseline** PodSecurity. In
a **restricted** namespace, either set `postgresql.podSecurityContext` /
`redis.podSecurityContext` to run as the image's non-root uid (with a matching
`fsGroup` so the data volume is writable), or — recommended — disable the bundled
datastores and use managed ones via `externalDatabase` / `externalRedis`. The app,
worker, and migrate pods are already locked down (nonroot, read-only rootfs).

## Values

See [`values.yaml`](values.yaml) — every key is documented inline. The most
commonly set:

| Key | Default | Purpose |
| --- | --- | --- |
| `image.repository` / `image.tag` | `ghcr.io/spacefleet/app` / chart appVersion | app image |
| `replicaCount` | `2` | web replicas (when HPA off) |
| `config.oidc.issuer` | `""` | external OIDC issuer (overrides bundled Dex) |
| `dex.enabled` | `false` | bundle Dex as the OIDC provider |
| `dex.storage` | `crd` | Dex storage: `crd` / `postgres` / `memory` / `sqlite3` |
| `dex.connectors` | `[]` | upstream connectors (GitHub, Google, …) |
| `config.workerConcurrency` | `4` | worker parallelism |
| `worker.enabled` | `true` | deploy the background worker |
| `migrations.enabled` | `true` | run `migrate up` on install/upgrade |
| `ingress.enabled` | `false` | expose via Ingress |
| `web.autoscaling.enabled` | `false` | HPA for the web tier |
| `postgresql.enabled` / `redis.enabled` | `true` | bundle first-party datastores |
| `postgresql.persistence.size` / `redis.persistence.size` | `8Gi` | bundled-datastore PVC size |
| `externalDatabase.*` / `externalRedis.*` | — | point at managed datastores |

## Publishing

The `helm-chart` CI job (in [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml))
packages and pushes the chart on `v*` tags using `GITHUB_TOKEN`. Two one-time
setup notes for maintainers:

- **Package visibility.** GHCR packages created by Actions start **private**.
  After the first tagged release, set the `charts/spacefleet` package to
  **public** (Org → Packages → spacefleet → Package settings) so users can
  `helm install` without authenticating. The `app` image package needs the same.
- **Versioning.** Tag the repo (`git tag vX.Y.Z && git push --tags`); the job
  derives both chart `version` and `appVersion` from the tag. No manual edits to
  `Chart.yaml` are needed — its committed `0.0.0` values are placeholders.

## Local development

Fetch the chart dependency (the dex subchart) once, then lint/render:

```sh
cd deploy/charts/spacefleet
helm dependency build
helm lint . -f ci/default-values.yaml
helm template rel . -f ci/dex-values.yaml | less   # exercises the bundled-Dex path
```

Or from the repo root: `make helm-lint` / `make helm-template` (both run
`helm dependency build` for you via the `helm-deps` target).
