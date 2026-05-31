---
title: Install & Configure with Helm
description: Deploy Spacefleet to Kubernetes with the official Helm chart — from a one-command trial to a production setup with external datastores, OIDC, and Ingress.
category: Operator
tags: [helm, kubernetes, install, configuration, oidc, postgres, redis]
---

# Install & Configure with Helm

Spacefleet ships as a single binary and is deployed to Kubernetes with an
official Helm chart published to GitHub Container Registry (GHCR). The chart
deploys everything you need to run the app:

- **web** — the `serve` process: the HTTP API plus the embedded React SPA.
- **worker** — the `worker` process: River background jobs.
- **migrate** — a one-shot Job that runs database migrations on install/upgrade.
- **Postgres** and **Redis** — bundled by default so a single command yields a
  working app, or point at your own managed services for production.

This guide takes you from a quick trial to a production-ready deployment.

## Prerequisites

- A Kubernetes cluster (v1.23+) and `kubectl` configured to reach it.
- [Helm 3.8+](https://helm.sh/docs/intro/install/) (OCI support is required and
  is on by default in 3.8+).
- A storage class that can provision PersistentVolumes if you use the bundled
  Postgres/Redis (most managed clusters have a default).

The chart is published as an OCI artifact, so there is **no `helm repo add`
step**. You reference it directly by its registry URL:

```
oci://ghcr.io/spacefleet/charts/spacefleet
```

The chart `version` always matches the app image tag, so `--version X.Y.Z`
always pairs with image `:X.Y.Z`. Replace `X.Y.Z` below with the
[release](https://github.com/spacefleet/app/releases) you want.

## Quick start (trial)

The fastest way to see Spacefleet running. This uses the **bundled** Postgres
and Redis and leaves authentication in dev mode:

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z
```

Then reach the app by port-forwarding the service:

```sh
kubectl port-forward svc/spacefleet 8080:80
# open http://localhost:8080
```

Confirm the deployment is healthy:

```sh
helm test spacefleet
```

> ⚠️ **Not for production.** With no OIDC issuer set, the backend runs a **dev
> passthrough that authenticates every request as `dev-user`** — anyone who can
> reach the app is effectively an admin. The bundled datastores also use a
> default password. The [Production deployment](#production-deployment) section
> below fixes both. The chart prints warnings after install whenever either of
> these is still in its insecure default.

## Production deployment

A real deployment changes three things from the trial:

1. **Authentication** — set your OIDC issuer so requests are actually verified.
2. **Datastores** — use a managed/HA Postgres and Redis instead of the bundled
   single-replica ones.
3. **Networking** — expose the app through an Ingress with TLS.

### 1. Configure authentication (OIDC)

You have two options — see [Authentication](authentication.md) for the full
walkthrough.

**Option A — bundle Dex** (no separate identity system to run):

```sh
--set dex.enabled=true \
--set ingress.enabled=true \
--set ingress.hosts[0].host=spacefleet.example.com
```

This deploys a self-contained OIDC provider served same-origin at
`https://spacefleet.example.com/dex`, seeded with an `admin@example.com` login
you must change. To add "Log in with GitHub/Google/…", change the seeded admin,
or choose where Dex stores its data, see
[Authenticate with the bundled Dex](authentication-with-dex.md).

**Option B — connect your own provider** (Keycloak, Auth0, Okta, your own Dex, …):

```sh
--set config.oidc.issuer=https://auth.example.com \
--set config.oidc.clientID=spacefleet
```

These two values are **not secrets** — they are also surfaced to the browser via
`/config.js` so the SPA can drive the login flow. Register a **public client**
(Authorization Code + PKCE) with your provider, and add the app's redirect URI
(`https://spacefleet.example.com/auth/callback`) to that client's allowed
redirect URIs. `config.oidc.issuer` takes precedence over bundled Dex.

Until one of these is configured, the backend stays in the insecure dev
passthrough — so this is the single most important thing to set before exposing
the deployment.

### 2. Use external datastores

For production, disable the bundled StatefulSets and point at managed services.
The recommended approach keeps credentials out of your Helm values and release
history by referencing **existing Secrets** you create yourself:

```sh
# Create Secrets holding the connection strings.
kubectl create secret generic spacefleet-db \
  --from-literal=DATABASE_URL='postgres://user:pass@db.example.com:5432/spacefleet?sslmode=require'

kubectl create secret generic spacefleet-redis \
  --from-literal=REDIS_URL='redis://:pass@redis.example.com:6379/0'
```

```sh
--set postgresql.enabled=false \
--set externalDatabase.existingSecret=spacefleet-db \
--set redis.enabled=false \
--set externalRedis.existingSecret=spacefleet-redis
```

There are three ways to supply each datastore's connection string:

| Mode | How | When to use |
| --- | --- | --- |
| **Bundled** (default) | `postgresql.enabled=true` / `redis.enabled=true` | Trials and small deployments. Single-replica, not HA. |
| **External, inline URL** | `externalDatabase.url=…` / `externalRedis.url=…` | Quick external setup; URL ends up in the release's Secret. |
| **External, existing Secret** | `externalDatabase.existingSecret=…` / `externalRedis.existingSecret=…` | **Recommended for production** — credentials stay in a Secret you control. |

If a datastore is neither bundled nor externally configured, the chart fails
templating with an explanatory error rather than deploying something broken.

> If you keep the bundled datastores for a small deployment, **always override
> the default passwords**: `--set postgresql.auth.password=… --set
> redis.auth.password=…`.

### 3. Expose the app via Ingress

```sh
--set ingress.enabled=true \
--set ingress.className=nginx \
--set ingress.hosts[0].host=spacefleet.example.com \
--set ingress.hosts[0].paths[0].path=/ \
--set ingress.hosts[0].paths[0].pathType=Prefix
```

For TLS, configure `ingress.tls` and (commonly) a cert-manager annotation. This
is much easier to express in a values file — see below.

### Putting it together with a values file

Long `--set` chains get unwieldy. For production, prefer a values file:

```yaml
# values.prod.yaml
config:
  oidc:
    issuer: https://auth.example.com
    clientID: spacefleet

postgresql:
  enabled: false
externalDatabase:
  existingSecret: spacefleet-db

redis:
  enabled: false
externalRedis:
  existingSecret: spacefleet-redis

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
  hosts:
    - host: spacefleet.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: spacefleet-tls
      hosts:
        - spacefleet.example.com

# Run multiple web replicas (or enable autoscaling below).
replicaCount: 3
web:
  autoscaling:
    enabled: true
    minReplicas: 3
    maxReplicas: 10
    targetCPUUtilizationPercentage: 80
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      memory: 256Mi
```

Install or upgrade with it:

```sh
helm upgrade --install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  -f values.prod.yaml
```

## How the pieces fit together

| Resource | Created when | Notes |
| --- | --- | --- |
| `Deployment/spacefleet-web` | always | `serve`; HTTP `:8080`, `/api/health` probes |
| `Deployment/spacefleet-worker` | `worker.enabled` | `worker`; River background jobs |
| `Job/spacefleet-migrate` | `migrations.enabled` | `migrate up`, a `post-install,pre-upgrade` hook |
| `Service/spacefleet` | always | ClusterIP → web |
| `Ingress/spacefleet` | `ingress.enabled` | external access |
| `HorizontalPodAutoscaler` | `web.autoscaling.enabled` | scales the web tier |
| `Secret/spacefleet-env` | unless both URLs come from existing Secrets | holds `DATABASE_URL` / `REDIS_URL` |
| `StatefulSet/spacefleet-postgresql` | `postgresql.enabled` | bundled Postgres |
| `StatefulSet/spacefleet-redis` | `redis.enabled` | bundled Redis |

**Migrations** run as a Helm hook. On a fresh install the Job runs
`post-install` (not `pre-install`) so it can reach the bundled Postgres, which is
a normal release resource created after pre-install hooks would have run. On
upgrades it runs `pre-upgrade`, before the new code rolls out.
`migrations.backoffLimit` (default `6`) covers the window while the database
becomes reachable.

## Commonly configured values

The chart documents every value it accepts inline; print the full, annotated
list with `helm show values` (shown at the end of this section). The keys you'll
reach for most:

| Key | Default | Purpose |
| --- | --- | --- |
| `config.oidc.issuer` | `""` | external OIDC issuer URL (overrides bundled Dex) |
| `config.oidc.clientID` | `spacefleet` | OIDC client ID the app uses |
| `dex.enabled` | `false` | bundle Dex as the OIDC provider (needs a hostname) |
| `dex.storage` | `crd` | Dex storage backend — `crd` keeps state in-cluster |
| `dex.connectors` | `[]` | upstream logins (GitHub, Google, …) for bundled Dex |
| `config.workerConcurrency` | `4` | max parallel background jobs |
| `config.extraEnv` | `[]` | extra env vars for web + worker pods |
| `image.repository` / `image.tag` | `ghcr.io/spacefleet/app` / chart appVersion | app image |
| `replicaCount` | `2` | web replicas (when autoscaling off) |
| `worker.enabled` | `true` | deploy the background worker |
| `migrations.enabled` | `true` | run `migrate up` on install/upgrade |
| `web.autoscaling.enabled` | `false` | HPA for the web tier |
| `ingress.enabled` | `false` | expose via Ingress |
| `postgresql.enabled` / `redis.enabled` | `true` | bundle first-party datastores |
| `postgresql.auth.password` / `redis.auth.password` | `spacefleet` | **change for bundled prod** |
| `postgresql.persistence.size` / `redis.persistence.size` | `8Gi` | bundled-datastore PVC size |
| `externalDatabase.*` / `externalRedis.*` | — | point at managed datastores |

Inspect everything the chart accepts with:

```sh
helm show values oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z
```

## Upgrading

```sh
helm upgrade spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  -f values.prod.yaml
```

Each upgrade runs the migration Job (`pre-upgrade`) before rolling out new web
and worker pods, so schema changes are applied before the new code starts.

## Uninstalling

```sh
helm uninstall spacefleet
```

> **PersistentVolumeClaims for the bundled datastores are not deleted** by
> `helm uninstall` — this protects your data from an accidental removal. If you
> truly want to discard the data, delete the PVCs manually:
>
> ```sh
> kubectl delete pvc -l app.kubernetes.io/instance=spacefleet
> ```

## Troubleshooting

**A warning about the dev passthrough after install.** `config.oidc.issuer` is
still empty. Set it (see [Configure authentication](#1-configure-authentication-oidc))
before exposing the app.

**A warning about default datastore passwords.** You're using the bundled
Postgres/Redis with their default `spacefleet` password. Override
`postgresql.auth.password` / `redis.auth.password`, or move to external
datastores.

**The migrate Job keeps retrying / pods are stuck `Pending`.** Usually the
database isn't reachable yet, or no storage class can satisfy a PVC. Check:

```sh
kubectl get pods
kubectl logs job/spacefleet-migrate
kubectl get pvc
```

**Restricted PodSecurity rejects the bundled datastores.** The official
Postgres/Redis images start as root and drop privileges, which is fine under
**baseline** PodSecurity but rejected under **restricted**. Either set
`postgresql.podSecurityContext` / `redis.podSecurityContext` to run as the
image's non-root user (with a matching `fsGroup` so the data volume is
writable), or — recommended — use external managed datastores. The app, worker,
and migrate pods are already locked down (nonroot, read-only root filesystem).

**`helm install` can't pull the chart.** Make sure you're on Helm 3.8+ and using
the full `oci://ghcr.io/spacefleet/charts/spacefleet` URL with `--version`. If
the package is private you'll need to `helm registry login ghcr.io` first.

## See also

- [Authentication](authentication.md) — how sign-in works and the ways to
  provide a login, so the deployment isn't left in the insecure dev mode.
- [Authenticate with the bundled Dex](authentication-with-dex.md) — enable the
  bundled provider, add GitHub/Google logins, and harden it.
- `helm show values oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z`
  — the complete, annotated list of every value the chart accepts.
