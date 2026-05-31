---
title: Authenticate with the bundled Dex
description: Enable Spacefleet's bundled Dex OIDC provider — sign in with the seeded admin, add "Log in with GitHub/Google", choose where Dex stores its data, and harden it for production.
category: Operator
tags: [authentication, dex, oidc, github, google, connectors, helm, security]
---

# Authenticate with the bundled Dex

Spacefleet's Helm chart can deploy [Dex](https://dexidp.io/) — a small,
self-contained OIDC provider — alongside the app. This gives you real
authentication without running or paying for a separate identity system, and
it's the recommended default for most deployments.

The bundled Dex can:

- authenticate against logins your users **already have** — GitHub, Google,
  LDAP, or any other OIDC provider — via a "connector"; and/or
- manage its own **built-in accounts** (handy for a first admin or a small team).

If you already run your own identity provider, you may prefer to point Spacefleet
straight at it instead — see
[Connect your own identity provider](authentication.md#connect-your-own-identity-provider).
For how authentication works in general, see [Authentication](authentication.md).

> **You need a hostname.** The bundled Dex is served on the **same address as the
> app** (under `/dex`), so it needs an Ingress host (or an explicitly set
> provider URL). With neither, the install stops with an explanatory message.

## Enable it

Turn Dex on and give Spacefleet a hostname:

```sh
helm upgrade --install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  --set dex.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

That's it — Spacefleet now has real logins. What you get:

- **Same-origin provider.** Dex is served at `https://spacefleet.example.com/dex`,
  on the app's own address. There's nothing extra to expose and no cross-origin
  ("allowed origins") setup, because the browser never leaves the app's domain.
- **In-cluster verification.** Spacefleet checks tokens against Dex *inside* the
  cluster, so sign-in keeps working even when the public URL isn't reachable from
  within the cluster (a common gotcha with internal load balancers).
- **A seeded admin login.** A fresh install includes one built-in account,
  **`admin@example.com` / `password`**, so you can sign in right away.

> ⚠️ **Change the seeded admin before exposing Spacefleet.** `admin@example.com`
> / `password` is a publicly known default — anyone could use it. Do this before
> the deployment is reachable: see [Change the admin login](#change-or-remove-the-admin-login).

To use a dedicated hostname for Dex instead of the same-origin path, set
`dex.issuer` (e.g. `https://login.example.com`); you'll then also set
`dex.web.allowedOrigins` to the app's address so the browser can call it.

## Change or remove the admin login

The seeded admin is meant to get you in the door, not to stay. You have two good
end states; pick one before exposing the deployment.

**Set your own password.** Generate a bcrypt hash and replace the seeded entry:

```sh
htpasswd -bnBC 10 "" 'your-strong-password' | tr -d ':\n'
```

```yaml
dex:
  enabled: true
  staticPasswords:
    - email: you@example.com
      hash: "<paste the hash>"
      username: you
      userID: "<any stable UUID>"
```

**Or remove built-in accounts entirely** once a connector is set up (recommended)
— let everyone sign in with GitHub/Google/etc.:

```yaml
dex:
  enabled: true
  staticPasswords: []   # no built-in accounts
```

Apply with `helm upgrade`. Built-in accounts and connectors can coexist — keep a
break-glass admin password if you like.

## Add "Log in with GitHub"

Let people sign in with their GitHub account, optionally restricted to a GitHub
organization.

1. **Create a GitHub OAuth app.** In GitHub → *Settings → Developer settings →
   OAuth Apps → New OAuth App*. Set:
   - **Homepage URL:** `https://spacefleet.example.com`
   - **Authorization callback URL:** `https://spacefleet.example.com/dex/callback`
     — this is **Dex's** callback (the issuer URL + `/callback`), not the app's
     `/auth/callback`.

   Copy the **Client ID** and generate a **client secret**.

2. **Store the secret in the cluster** (keep it out of your Helm values):

   ```sh
   kubectl create secret generic spacefleet-dex-connectors \
     --namespace <your namespace> \
     --from-literal=GITHUB_CLIENT_SECRET='<the client secret>'
   ```

3. **Configure the connector.** Reference the secret by environment variable
   (`$GITHUB_CLIENT_SECRET`); Dex substitutes it at load time:

   ```yaml
   dex:
     enabled: true
     connectors:
       - type: github
         id: github
         name: GitHub
         config:
           clientID: <your GitHub OAuth client id>
           clientSecret: $GITHUB_CLIENT_SECRET
           # Restrict who can sign in (omit to allow any GitHub user):
           orgs:
             - name: your-org
             # - name: your-org
             #   teams: [platform, sre]   # restrict further to teams
     envFrom:
       - secretRef:
           name: spacefleet-dex-connectors
   ```

4. **Apply and sign in:** `helm upgrade …`. The login page now offers "Log in
   with GitHub." First sign-in provisions the user automatically.

## Add "Log in with Google"

1. **Create an OAuth client** in the [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
   → *Credentials → Create credentials → OAuth client ID → Web application*. Set
   the **Authorized redirect URI** to `https://spacefleet.example.com/dex/callback`.
   Copy the **Client ID** and **client secret**.

2. **Store the secret:**

   ```sh
   kubectl create secret generic spacefleet-dex-connectors \
     --namespace <your namespace> \
     --from-literal=GOOGLE_CLIENT_SECRET='<the client secret>'
   ```

3. **Configure the connector** (the generic OIDC connector against Google works
   well for basic sign-in):

   ```yaml
   dex:
     enabled: true
     connectors:
       - type: oidc
         id: google
         name: Google
         config:
           issuer: https://accounts.google.com
           clientID: <your Google client id>
           clientSecret: $GOOGLE_CLIENT_SECRET
           redirectURI: https://spacefleet.example.com/dex/callback
     envFrom:
       - secretRef:
           name: spacefleet-dex-connectors
   ```

4. **Apply and sign in.** To restrict to a Google Workspace domain or surface
   Google Groups, use Dex's dedicated `google` connector instead — see the Dex
   docs below.

## Other connectors

Dex supports many upstream providers — LDAP, SAML, Microsoft, GitLab, generic
OIDC, and more. They all follow the same shape: register an app with the upstream
provider using `https://spacefleet.example.com/dex/callback` as the callback,
store any secret in a Kubernetes Secret, and add the provider's block to
`dex.connectors` (referencing the secret as `$VAR` via `dex.envFrom`). The chart
passes your connector configuration through to Dex unchanged. See the full list
and per-connector options in the
[Dex connector documentation](https://dexidp.io/docs/connectors/).

## Choose where Dex stores its data

Dex persists its signing keys and sessions. The backend is set with
`dex.storage`:

| `dex.storage` | What it does | Use when |
| --- | --- | --- |
| `crd` *(default)* | Stores state as Kubernetes resources in the cluster. Durable across restarts, works with multiple replicas, **no database**. | Almost always — the recommended default. |
| `postgres` | Stores state in a PostgreSQL database you point it at. | You'd rather keep auth state in a database, or run Dex at high volume. |
| `memory` | Keeps everything in memory. **Resets on every restart** (new signing keys → everyone re-logs in) and can't run more than one replica. | Quick trials only. |
| `sqlite3` | A single file on a persistent volume. | Single-replica setups that want a file-based store. |

The default `crd` needs Dex to manage a couple of cluster-scoped resources; the
chart grants that automatically. If you switch to `postgres`/`memory`/`sqlite3`
you can turn the cluster-scoped permission off with
`dex.rbac.createClusterScoped=false`.

**Example — PostgreSQL storage.** Store the DB password in a Secret (added via
`dex.envFrom`, as with connectors) and point Dex at the database:

```yaml
dex:
  enabled: true
  storage: postgres
  storageConfig:
    host: postgres.example.com
    port: 5432
    database: dex
    user: dex
    password: $DEX_DB_PASSWORD
    ssl:
      mode: require
  rbac:
    createClusterScoped: false
```

## Register additional applications

If another service needs to authenticate against the same Dex, add it to
`dex.extraStaticClients` (passed straight through to Dex):

```yaml
dex:
  extraStaticClients:
    - id: my-other-app
      name: My Other App
      redirectURIs:
        - https://other.example.com/callback
      secretEnv: MY_OTHER_APP_CLIENT_SECRET   # provided via dex.envFrom
```

Spacefleet's own client is registered for you; you don't need to list it here.

## Going to production

Before exposing the deployment, confirm:

- [ ] **The seeded admin is changed or removed** ([guide](#change-or-remove-the-admin-login)).
- [ ] **A connector or real passwords** are configured — not the default login.
- [ ] **Storage is `crd` or `postgres`**, not `memory` (which loses sessions on
      restart).
- [ ] **TLS is set on the Ingress** so the issuer is `https://…` (set
      `ingress.tls`).
- [ ] **Connectors are scoped** to your organization/domain rather than open to
      every account on the upstream provider.
- [ ] *(Optional)* **More than one Dex replica** for high availability — `crd`
      and `postgres` storage support it; set `dex.replicaCount`.

## Troubleshooting

**Install stops with "Bundled Dex needs a public issuer URL."** Dex has no
hostname to use. Enable Ingress (`ingress.enabled=true` + a host) so the issuer
becomes `https://<host>/dex`, or set `dex.issuer` for a dedicated host.

**Install stops with "no redirect URIs for the app's login callback."** Enable
Ingress so the app's public address is known, or set `dex.extraRedirectURIs`.

**GitHub/Google rejects the login with a redirect-URI mismatch.** The callback
registered with the upstream provider must be **Dex's** callback —
`https://<your host>/dex/callback` — not the app's `/auth/callback`. Update it at
the provider.

**The connector's secret isn't being picked up.** Make sure `dex.envFrom`
references the Secret that holds it, and that the `$VAR` name in the connector
config matches the key in that Secret exactly.

**The Dex pod won't start with `crd` storage.** It needs permission to manage its
cluster-scoped resources. Keep `dex.rbac.create=true` and
`dex.rbac.createClusterScoped=true` (both default on) for `crd` storage.

**Everyone is logged out after a Dex restart.** You're on `memory` storage, which
regenerates signing keys on restart. Switch `dex.storage` to `crd` (or
`postgres`).

For symptoms that aren't specific to Dex (audience mismatch, redirect loops,
"Not found" after login), see the
[Authentication troubleshooting](authentication.md#troubleshooting) section.

## See also

- [Authentication](authentication.md) — how sign-in works and the other ways to
  provide a login.
- [Install & configure with Helm](install-with-helm.md) — the rest of a
  Kubernetes deployment.
- [Dex documentation](https://dexidp.io/docs/) — connectors, storage backends,
  and advanced configuration.
