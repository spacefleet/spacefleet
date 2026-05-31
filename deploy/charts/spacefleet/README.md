# Spacefleet Helm chart

Deploys [Spacefleet](https://github.com/spacefleet/app) — the Go + React
single-binary app — to Kubernetes: the `serve` web/API process, the `worker`
background-job process, and a `migrate up` release hook, plus optional bundled
Postgres and Redis.

The chart is published as an **OCI artifact to GHCR** by the same CI pipeline
that builds the image, and its `version`/`appVersion` track the app's `v*` git
tags — so `--version X.Y.Z` always pairs with image `:X.Y.Z`.

## Install

Out-of-the-box trial (bundled Postgres + Redis, dev passthrough auth):

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z
```

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

The migrate Job runs `post-install` (not `pre-install`) so it can reach the
bundled Postgres, which is a normal release resource and therefore created
after pre-install hooks would run. On upgrades it runs `pre-upgrade`, before
the new web/worker code rolls out. `migrations.backoffLimit` covers the window
while Postgres becomes reachable.

The bundled datastores are **single-replica StatefulSets running the official
upstream images** (the same `postgres:17-alpine` / `redis:7-alpine` as the dev
`docker-compose.yml`) — there are no third-party chart dependencies, so there's
no `helm dependency build` step and nothing to break when an upstream chart repo
changes. They're meant for trials and small deployments, not HA; for production
use a managed/HA datastore via `externalDatabase` / `externalRedis`.

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

`config.oidc.issuer` is **empty by default**, which leaves the backend in its
dev passthrough that authenticates every request as `dev-user`. Set it to your
OIDC provider (Dex, Keycloak, Auth0, …) before exposing the deployment;
`config.oidc.issuer` + `config.oidc.clientID` are non-secret and are also
surfaced to the browser via `/config.js`.

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
| `config.oidc.issuer` | `""` | OIDC issuer — **set for production** |
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

The chart has no external dependencies, so there's no `helm dependency` step:

```sh
cd deploy/charts/spacefleet
helm lint . -f ci/default-values.yaml
helm template rel . -f ci/default-values.yaml | less
```

Or from the repo root: `make helm-lint` / `make helm-template`.
