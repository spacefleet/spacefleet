---
title: Install & Configure with Helm
description: Deploy Spacefleet to Kubernetes with the official Helm chart — from a one-command trial to a production setup with an external database, OIDC, and Ingress.
category: Operator
tags: [helm, kubernetes, install, configuration, oidc, postgres]
---

# Install & Configure with Helm

Spacefleet ships as a single binary and is deployed to Kubernetes with an
official Helm chart published to GitHub Container Registry (GHCR). The chart
deploys everything you need to run the app:

- **web** — the `serve` process: the HTTP API plus the embedded React SPA.
- **worker** — the `worker` process: River background jobs.
- **migrate** — a one-shot Job that runs database migrations on install/upgrade.
- **Postgres** — bundled by default so a single command yields a working app, or
  point at your own managed database for production.
- **Dex (OIDC)** — the bundled identity provider, always deployed as part of the
  platform so you get real logins without running one separately. The app serves
  it same-origin under `/dex`; you configure who can sign in. See
  [Authentication](authentication.md).

This guide takes you from a quick trial to a production-ready deployment.

## Prerequisites

- A Kubernetes cluster (v1.23+) and `kubectl` configured to reach it.
- [Helm 3.8+](https://helm.sh/docs/intro/install/) (OCI support is required and
  is on by default in 3.8+).
- A storage class that can provision PersistentVolumes if you use the bundled
  Postgres (most managed clusters have a default).

The chart is published as an OCI artifact, so there is **no `helm repo add`
step**. You reference it directly by its registry URL:

```
oci://ghcr.io/spacefleet/charts/spacefleet
```

The chart `version` always matches the app image tag, so `--version X.Y.Z`
always pairs with image `:X.Y.Z`. Replace `X.Y.Z` below with the
[release](https://github.com/spacefleet/spacefleet/releases) you want.

## Quick start (trial)

The fastest way to see Spacefleet running. This uses the **bundled** Postgres
and Dex, with no Ingress:

```sh
helm install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z
```

Then reach the app by port-forwarding the service:

```sh
kubectl port-forward svc/spacefleet 8080:80
# open http://localhost:8080
```

Sign in with the seeded admin login **`admin@example.com` / `password`**, then
confirm the deployment is healthy:

```sh
helm test spacefleet
```

> ⚠️ **Not for production.** Without an Ingress host, the bundled login provider
> falls back to a `localhost` address that only works through this
> port-forward — fine for a trial, not for exposing the app. The seeded admin
> (`admin@example.com` / `password`) is a publicly known credential, and the
> bundled datastores use a default password. The
> [Production deployment](#production-deployment) section below fixes all three;
> the chart prints warnings after install whenever any insecure default remains.

## Production deployment

A real deployment changes three things from the trial:

1. **Authentication** — give the bundled login provider a real hostname and
   replace the seeded admin login.
2. **Database** — use a managed/HA Postgres instead of the bundled
   single-replica one.
3. **Networking** — expose the app through an Ingress with TLS.

### 1. Configure authentication (OIDC)

Spacefleet's identity provider (Dex) is always bundled — you don't point the app
at an external one. For production you give it a real hostname (so its address
becomes `https://…` instead of the localhost trial fallback) and decide who can
sign in. The hostname comes from your Ingress:

```sh
--set ingress.enabled=true \
--set ingress.hosts[0].host=spacefleet.example.com
```

The login provider is then served same-origin at
`https://spacefleet.example.com/dex`, seeded with an `admin@example.com` login
**you must change before exposing the app**. To add "Log in with
GitHub/Google/Okta/Entra/LDAP/…", change or remove the seeded admin, or choose
where login state is stored, see [Authentication](authentication.md).

Setting a real hostname (so logins don't fall back to localhost) and replacing
the seeded admin are the single most important things to do before exposing the
deployment.

### 2. Use an external database

For production, disable the bundled StatefulSet and point at a managed service.
The recommended approach keeps credentials out of your Helm values and release
history by referencing an **existing Secret** you create yourself:

```sh
# Create a Secret holding the connection string.
kubectl create secret generic spacefleet-db \
  --from-literal=DATABASE_URL='postgres://user:pass@db.example.com:5432/spacefleet?sslmode=require'
```

```sh
--set postgresql.enabled=false \
--set externalDatabase.existingSecret=spacefleet-db
```

There are three ways to supply the database connection string:

| Mode | How | When to use |
| --- | --- | --- |
| **Bundled** (default) | `postgresql.enabled=true` | Trials and small deployments. Single-replica, not HA. |
| **External, inline URL** | `externalDatabase.url=…` | Quick external setup; URL ends up in the release's Secret. |
| **External, existing Secret** | `externalDatabase.existingSecret=…` | **Recommended for production** — credentials stay in a Secret you control. |

If the database is neither bundled nor externally configured, the chart fails
templating with an explanatory error rather than deploying something broken.

For the full Postgres connection-string reference — TLS modes, enforcing full
certificate validation, and mounting a managed provider's CA bundle (e.g. Amazon
RDS) — see [Database configuration](database.md).

> If you keep the bundled database for a small deployment, **always override
> the default password**: `--set postgresql.auth.password=…`.

### 3. Set the credential-encryption key

Spacefleet encrypts credentials it stores (such as a registered cluster's token
or kubeconfig) with a key you provide as `SPACEFLEET_SECRET_KEY` — a
base64-encoded 32-byte key. Without it, registering anything that carries a
credential fails with a clear error; other features keep working. As with the
database, the recommended approach keeps the key out of your values by putting
it in a Secret you control:

```sh
kubectl create secret generic spacefleet-app-secrets \
  --from-literal=SPACEFLEET_SECRET_KEY="$(openssl rand -base64 32)"
```

```sh
--set config.secrets.envFrom[0].secretRef.name=spacefleet-app-secrets
```

> ⚠️ **This key cannot be rotated in place** — changing it makes already-encrypted
> data unreadable. Generate it once and back it up. For the inline (trial)
> alternative and details, see [Secret configuration](secrets.md).

### 4. Expose the app via Ingress

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
# The bundled Dex derives its issuer from the ingress host below. To replace the
# seeded admin or add GitHub/Google/etc. logins, set dex.* — see
# "Authenticate with the bundled Dex".

postgresql:
  enabled: false
externalDatabase:
  existingSecret: spacefleet-db

config:
  # Secret app config (the credential-encryption key, and anything added later)
  # comes from a Secret you manage — see "Set the credential-encryption key".
  secrets:
    envFrom:
      - secretRef:
          name: spacefleet-app-secrets

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
| `Secret/spacefleet-env` | when the chart owns `DATABASE_URL` and/or an inline `config.secrets.secretKey` | holds those values; absent when both come from Secrets you manage |
| `StatefulSet/spacefleet-postgresql` | `postgresql.enabled` | bundled Postgres |
| `Deployment/spacefleet-dex` (+ Service, RBAC, config Secret) | always | bundled Dex OIDC provider; internal ClusterIP, reached via the app's `/dex` proxy |

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
| `config.oidc.clientID` | `spacefleet` | OIDC client ID the app uses (keep in sync with `dex.clientID`) |
| `config.secrets.envFrom` | `[]` | load secret env (e.g. `SPACEFLEET_SECRET_KEY`) from Secrets you manage — see [Secret configuration](secrets.md) |
| `dex.storage` | `crd` | Dex storage backend — `crd` keeps state in-cluster |
| `dex.connectors` | `[]` | upstream logins (GitHub, Google, Okta, LDAP, …) |
| `dex.staticPasswords` | seeded admin | built-in accounts — **change before exposing** |
| `config.workerConcurrency` | `4` | max parallel background jobs |
| `config.extraEnv` | `[]` | extra env vars for web + worker pods |
| `image.repository` / `image.tag` | `ghcr.io/spacefleet/spacefleet` / chart appVersion | app image |
| `replicaCount` | `2` | web replicas (when autoscaling off) |
| `worker.enabled` | `true` | deploy the background worker |
| `migrations.enabled` | `true` | run `migrate up` on install/upgrade |
| `web.autoscaling.enabled` | `false` | HPA for the web tier |
| `ingress.enabled` | `false` | expose via Ingress |
| `postgresql.enabled` | `true` | bundle the first-party database |
| `postgresql.auth.password` | `spacefleet` | **change for bundled prod** |
| `postgresql.persistence.size` | `8Gi` | bundled-database PVC size |
| `externalDatabase.*` | — | point at a managed database |

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

**A warning about the seeded admin / localhost issuer after install.** You're
still on insecure defaults: the bundled login provider has no Ingress host (so
it fell back to a localhost trial address) and/or the seeded `admin@example.com`
password is unchanged. Set an Ingress host and replace the seeded admin — see
[Configure authentication](#1-configure-authentication-oidc) — before exposing
the app.

**A warning about the default database password.** You're using the bundled
Postgres with its default `spacefleet` password. Override
`postgresql.auth.password`, or move to an external database.

**The migrate Job keeps retrying / pods are stuck `Pending`.** Usually the
database isn't reachable yet, or no storage class can satisfy a PVC. Check:

```sh
kubectl get pods
kubectl logs job/spacefleet-migrate
kubectl get pvc
```

**Restricted PodSecurity rejects the bundled database.** The official
Postgres image starts as root and drops privileges, which is fine under
**baseline** PodSecurity but rejected under **restricted**. Either set
`postgresql.podSecurityContext` to run as the image's non-root user (with a
matching `fsGroup` so the data volume is writable), or — recommended — use an
external managed database. The app, worker, and migrate pods are already locked
down (nonroot, read-only root filesystem).

**`helm install` can't pull the chart.** Make sure you're on Helm 3.8+ and using
the full `oci://ghcr.io/spacefleet/charts/spacefleet` URL with `--version`. If
the package is private you'll need to `helm registry login ghcr.io` first.

## See also

- [Authentication](authentication.md) — how sign-in works with the bundled
  identity provider, plus adding GitHub/Google logins, storage, and hardening.
- [Secret configuration](secrets.md) — the credential-encryption key and other
  secret settings (inline for a trial, or from a Secret you manage).
- [Database configuration](database.md) — connection string, TLS modes, and
  managed-provider CA bundles.
- `helm show values oci://ghcr.io/spacefleet/charts/spacefleet --version X.Y.Z`
  — the complete, annotated list of every value the chart accepts.
