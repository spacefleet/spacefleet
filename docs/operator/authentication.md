---
title: Authentication
description: How Spacefleet authenticates users with OpenID Connect (OIDC) — the always-bundled identity provider, signing in for the first time, adding "Log in with GitHub/Google", choosing where login state is stored, and hardening it for production.
category: Operator
tags: [authentication, oidc, sso, dex, identity-provider, github, google, connectors, configuration, security]
---

# Authentication

Spacefleet does not manage passwords. It delegates sign-in to an **OpenID
Connect (OIDC)** provider — the same standard behind "Log in with
Google/Okta/Entra." A provider handles the login; Spacefleet trusts the signed
result and checks it on every request.

That provider is **built in**. Every Spacefleet deployment ships with its own
identity provider ([Dex](https://dexidp.io/)) as an integral part of the
platform — you do not run, pay for, or point Spacefleet at a separate identity
system, and there is no way to turn it off. When your users already have logins
elsewhere (GitHub, Google, Okta, Entra ID, an LDAP directory, a SAML IdP), you
connect those **through** the built-in provider rather than replacing it.

This page covers the whole lifecycle: how sign-in works, getting logged in for
the first time, deciding who can log in, where login state is stored, and going
to production.

## How sign-in works

From a user's point of view, signing in to Spacefleet looks like any other
single sign-on app:

1. They open Spacefleet and are redirected to the login page.
2. They authenticate — a built-in account, or "Log in with GitHub/Google/…",
   whatever you've configured.
3. They're sent back to Spacefleet, already signed in.

Spacefleet never sees their password. It receives a **signed token** proving who
the user is, verifies that token's signature and claims on every request, and
keeps the session alive by renewing the token silently until the session ends.

The login provider is served on **Spacefleet's own web address**, under the
`/dex` path — the app forwards those requests to the provider internally. That
has a few welcome consequences:

- **One address to expose.** Only the app is reachable from outside; the
  provider rides along on the same hostname, and the browser never leaves the
  app's domain during login — so there's **no cross-origin / "allowed origins"
  setup** to get wrong.
- **In-cluster verification.** Spacefleet checks tokens against the provider
  *inside* the cluster, so sign-in keeps working even when the public URL isn't
  reachable from within the cluster (a common gotcha with internal load
  balancers).
- **Identity can live upstream.** If you connect an existing provider (a GitHub
  org, a Google Workspace, your corporate IdP), disabling a user or requiring
  MFA there applies to Spacefleet immediately — you manage access where you
  already manage everyone else.
- **Accounts are created on first login.** The first time someone signs in,
  Spacefleet creates a local record for them automatically from the token (their
  stable user ID and email). There is no separate user-import step.

## Sign in for the first time

A real deployment needs a **hostname**, because the login provider uses your
Ingress host for its `https://` address. Give Spacefleet one and install:

```sh
helm upgrade --install spacefleet oci://ghcr.io/spacefleet/charts/spacefleet \
  --version X.Y.Z \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=spacefleet.example.com
```

Spacefleet comes up with real logins, served same-origin at
`https://spacefleet.example.com/dex`, and a single seeded built-in account —
**`admin@example.com` / `password`** — so you can sign in right away.

Without an Ingress host, a fresh install still works but falls back to a
`localhost` address that's only reachable through `kubectl port-forward` — fine
for a quick trial, not for anything you expose.

> ⚠️ **Change the seeded admin before exposing Spacefleet.** `admin@example.com`
> / `password` is a publicly known default — anyone could use it. Do this before
> the deployment is reachable: see [Change or remove the admin login](#change-or-remove-the-admin-login).

## Decide who can log in

The only real decision is **who's allowed in**, which you make on the provider
via two mechanisms that can be used together:

- **Built-in accounts** — the seeded admin, plus any you add. Good for a first
  administrator or a small team.
- **Connectors** — "Log in with GitHub/Google/Okta/Entra/LDAP/SAML/…". Good for
  letting people use logins they already have, with access scoped to your org or
  domain.

Both can coexist — for example, a break-glass admin password alongside GitHub
sign-in.

### Change or remove the admin login

The seeded admin is meant to get you in the door, not to stay. You have two good
end states; pick one before exposing the deployment.

**Set your own password.** Generate a bcrypt hash and replace the seeded entry:

```sh
htpasswd -bnBC 10 "" 'your-strong-password' | tr -d ':\n'
```

```yaml
dex:
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
  staticPasswords: []   # no built-in accounts
```

Apply with `helm upgrade`. Built-in accounts and connectors can coexist — keep a
break-glass admin password if you like.

### Add "Log in with GitHub"

Let people sign in with their GitHub account, optionally restricted to a GitHub
organization.

1. **Create a GitHub OAuth app.** In GitHub → *Settings → Developer settings →
   OAuth Apps → New OAuth App*. Set:
   - **Homepage URL:** `https://spacefleet.example.com`
   - **Authorization callback URL:** `https://spacefleet.example.com/dex/callback`
     — this is the provider's callback (your address + `/dex/callback`), not the
     app's own `/auth/callback`.

   Copy the **Client ID** and generate a **client secret**.

2. **Store the secret in the cluster** (keep it out of your Helm values):

   ```sh
   kubectl create secret generic spacefleet-dex-connectors \
     --namespace <your namespace> \
     --from-literal=GITHUB_CLIENT_SECRET='<the client secret>'
   ```

3. **Configure the connector.** Reference the secret by environment variable
   (`$GITHUB_CLIENT_SECRET`); it's substituted at load time:

   ```yaml
   dex:
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

   You don't set the connector's own callback (`config.redirectURI`) — it's
   derived for you as `https://spacefleet.example.com/dex/callback` (your address
   + `/dex/callback`), the same URL you registered with GitHub in step 1. The two
   must match; that's why this is the address you give GitHub.

4. **Apply and sign in:** `helm upgrade …`. The login page now offers "Log in
   with GitHub." First sign-in provisions the user automatically.

### Add "Log in with Google"

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
     connectors:
       - type: oidc
         id: google
         name: Google
         config:
           issuer: https://accounts.google.com
           clientID: <your Google client id>
           clientSecret: $GOOGLE_CLIENT_SECRET
           # redirectURI is derived as https://spacefleet.example.com/dex/callback
           # — set it here only to override (e.g. if you front Dex differently).
     envFrom:
       - secretRef:
           name: spacefleet-dex-connectors
   ```

4. **Apply and sign in.** To restrict to a Google Workspace domain or surface
   Google Groups, use the dedicated `google` connector instead — see the
   [provider documentation](https://dexidp.io/docs/connectors/).

### Other connectors

Many more upstream providers are supported — LDAP, SAML, Microsoft, GitLab,
generic OIDC, and more. They all follow the same shape: register an app with the
upstream provider using `https://spacefleet.example.com/dex/callback` as the
callback, store any secret in a Kubernetes Secret, and add the provider's block
to `dex.connectors` (referencing the secret as `$VAR` via `dex.envFrom`). For
callback-based providers the connector's `config.redirectURI` is derived to that
same `…/dex/callback` URL automatically; directory connectors like LDAP that have
no callback are left as-is. Set `config.redirectURI` yourself only to override.
See the full list and per-connector options in the
[Dex connector documentation](https://dexidp.io/docs/connectors/).

## Choose where login state is stored

The provider persists its signing keys and sessions. The backend is set with
`dex.storage`:

| `dex.storage` | What it does | Use when |
| --- | --- | --- |
| `crd` *(default)* | Stores state as Kubernetes resources in the cluster. Durable across restarts, works with multiple replicas, **no database**. | Almost always — the recommended default. |
| `postgres` | Stores state in a PostgreSQL database you point it at. | You'd rather keep auth state in a database, or run at high volume. |
| `memory` | Keeps everything in memory. **Resets on every restart** (new signing keys → everyone re-logs in) and can't run more than one replica. | Quick trials only. |
| `sqlite3` | A single file on a persistent volume. | Single-replica setups that want a file-based store. |

The default `crd` needs a couple of cluster-scoped resources, which the chart
grants automatically. If you switch to `postgres`/`memory`/`sqlite3` you can turn
that permission off with `dex.rbac.createClusterScoped=false`.

**Example — PostgreSQL storage.** Store the DB password in a Secret (added via
`dex.envFrom`, as with connectors) and point the provider at the database:

```yaml
dex:
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

If another service needs to authenticate against the same provider, add it to
`dex.extraStaticClients`:

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
- [ ] **TLS is set on the Ingress** so the address is `https://…` (set
      `ingress.tls`).
- [ ] **Connectors are scoped** to your organization/domain rather than open to
      every account on the upstream provider.
- [ ] *(Optional)* **More than one replica** for high availability — `crd` and
      `postgres` storage support it; set `dex.replicaCount`.

## Troubleshooting

**Spacefleet won't start.** The server refuses to boot without a login provider
configured — there is no "run with authentication off" mode. On Kubernetes the
Helm chart wires this up for you; if you run the binary directly, it needs the
provider's address in its environment (the chart and the development setup both
set this automatically).

**Login only works from `localhost` / breaks once you expose the app.** You
installed without an Ingress host, so the login provider fell back to its
`localhost` trial address. Set `ingress.enabled=true` and an
`ingress.hosts[0].host` so the address becomes `https://<host>`, then
`helm upgrade`.

**Every request fails with "unauthorized" right after a change.** The token
audience doesn't match the client ID Spacefleet expects. If you've customized the
client ID, make sure `config.oidc.clientID` and `dex.clientID` match.

**Login bounces in a loop, or the login page rejects the redirect.** The address
users reach Spacefleet at doesn't match the one the provider was configured with.
Use a single, consistent address everywhere (scheme, host, and the
`/auth/callback` path), which for a real deployment means setting your Ingress
host.

**After logging in, users land on a "Not found" page.** The app was reached over
a different address than the one registered as the redirect target (for example
`http://` vs `https://`, or a bare IP vs the hostname). Use one consistent
address.

**GitHub/Google rejects the login with a redirect-URI mismatch.** The callback
registered with the upstream provider must be the provider's callback —
`https://<your host>/dex/callback` — not the app's `/auth/callback`. Update it at
the upstream provider.

**The connector's secret isn't being picked up.** Make sure `dex.envFrom`
references the Secret that holds it, and that the `$VAR` name in the connector
config matches the key in that Secret exactly.

**The provider pod won't start with `crd` storage.** It needs permission to
manage its cluster-scoped resources. Keep `dex.rbac.create=true` and
`dex.rbac.createClusterScoped=true` (both default on) for `crd` storage.

**Everyone is logged out after a restart.** You're on `memory` storage, which
regenerates signing keys on restart. Switch `dex.storage` to `crd` (or
`postgres`).

## See also

- [Install & configure with Helm](install-with-helm.md#1-configure-authentication-oidc)
  — where authentication fits in a Kubernetes deployment.
- [Dex documentation](https://dexidp.io/docs/) — the full list of connectors,
  storage backends, and advanced configuration.
