---
title: Private Git charts (GitHub App)
description: Let organizations deploy Helm charts from private Git repositories by registering a GitHub App for your Spacefleet deployment — creating the App, the permissions and callback settings it needs, supplying its credentials safely as Helm values, how an organization connects an installation, and troubleshooting the connect flow. Covers the operator setup, not how to use the git chart source.
category: Operator
tags: [github, github-app, git, helm, private-repositories, charts, applications, configuration, secrets]
---

# Private Git charts (GitHub App)

Spacefleet can deploy a Helm chart straight from a **Git repository**. Public
repositories work with no setup. To deploy from **private** repositories, you
register one **GitHub App** for your deployment; organizations then install that
App on their own repositories, and Spacefleet uses it to fetch the chart at
deploy time.

This integration is **optional**. With no GitHub App configured, the Git chart
source still works for **public** repositories — configuring an App only adds
the ability to reach private ones.

Why a GitHub App (rather than a personal token)? The access is scoped to the
repositories each organization chooses, is read-only, is revocable by the
organization at any time from GitHub, and is exchanged for a **short-lived**
token only at deploy time — Spacefleet never stores a long-lived Git
credential.

This page covers the operator side: creating the App, the settings to provide,
and supplying its private key safely. It does not cover *using* the Git chart
source — that's in the user documentation.

For where this fits in a full install, see
[Install & configure with Helm](install-with-helm.md).

## Before you start

You'll create the App once, as the operator of the deployment. You need:

- access to create a GitHub App (under a GitHub user or organization account),
- your deployment's **public base URL** (`config.externalURL`, e.g.
  `https://spacefleet.example.com`) — the App redirects back to it,
- a credential-encryption key configured (`SPACEFLEET_SECRET_KEY`) — it also
  signs the connect handshake, so the feature needs it. See
  [Secret configuration](secrets.md).

## Create the GitHub App

In GitHub, go to **Settings → Developer settings → GitHub Apps → New GitHub
App** (under the user or organization that should own the App), and set:

- **Name** — anything, e.g. "Acme Spacefleet".
- **Homepage URL** — your **plain** deployment URL (e.g.
  `https://spacefleet.example.com`). Cosmetic only.
- **Callback URL** (in the *"Identifying and authorizing users"* section) —
  `https://<your external URL>/github/callback`. It must be your
  `config.externalURL` with `/github/callback` appended. GitHub sends the user
  here *after they install the App*, and Spacefleet records the installation.
- **Request user authorization (OAuth) during installation** (same section) —
  **check it.** This is required: it makes GitHub include an authorization code
  in the redirect, which Spacefleet exchanges to confirm the person completing
  the installation really has access to it — that check is what stops one
  organization from claiming another organization's installation. (With this
  checked, GitHub disables the separate *Setup URL* field — that's expected;
  Spacefleet doesn't use it.)
- **Webhook** — **uncheck "Active".** Spacefleet doesn't use webhooks.
- **Repository permissions** → **Contents: Read-only.** This is the only
  permission needed (to read chart files); leave everything else at *No access*.
- **Where can this GitHub App be installed?** — your choice. "Only on this
  account" keeps it private to your organization; "Any account" lets other
  organizations install it too.

Create the App, then collect five things:

1. The **App ID** — shown on the App's settings page.
2. The App's **slug** — just the last path segment of its public page, e.g.
   `acme-spacefleet` from `https://github.com/apps/acme-spacefleet` (**not** the
   full URL). It's GitHub's URL-ified version of the App name. Spacefleet uses it
   to build the install link.
3. A **private key** — click **Generate a private key**; GitHub downloads a
   `.pem` file. This is the secret Spacefleet uses to authenticate as the App.
4. The **Client ID** — shown on the App's settings page (a `Iv1.…`-style id,
   not the numeric App ID).
5. A **client secret** — click **Generate a new client secret**. Together with
   the Client ID, Spacefleet uses it to verify the user completing an
   installation. It is a secret, like the private key.

## Settings

Spacefleet reads the following, set by the Helm chart under `config.github`:

| Helm value | Environment variable | Default | Purpose |
| --- | --- | --- | --- |
| `config.github.appId` | `GITHUB_APP_ID` | _(empty)_ | Numeric GitHub App ID. **Empty disables the feature.** |
| `config.github.slug` | `GITHUB_APP_SLUG` | _(empty)_ | App URL slug (`github.com/apps/<slug>`). **Empty disables the feature.** |
| `config.github.privateKey` | `GITHUB_APP_PRIVATE_KEY` | _(empty)_ | App private key (PEM) — **a secret**, see below. |
| `config.github.clientId` | `GITHUB_APP_CLIENT_ID` | _(empty)_ | App OAuth Client ID (`Iv1.…`-style). **Empty disables the feature.** |
| `config.github.clientSecret` | `GITHUB_APP_CLIENT_SECRET` | _(empty)_ | App OAuth client secret — **a secret**, see below. |

**The feature is enabled only when all five are set.** With any empty,
Spacefleet treats the GitHub App as not configured and the Git chart source
remains public-only. The client ID and secret are not optional extras: they are
how Spacefleet confirms, when an installation is connected, that the person
completing it actually has access to that installation.

> [!IMPORTANT]
> The `/github/callback` address goes in the App's **Callback URL** field (the
> *"Identifying and authorizing users"* section), and **Request user
> authorization (OAuth) during installation** must be checked. The URL must be
> `config.externalURL` + `/github/callback`. If a user finishes installing on
> GitHub and lands on a "not found" page or the connection fails, a wrong/empty
> Callback URL or an unchecked "Request user authorization" box is the usual
> cause.

## Configure it

Provide the App ID, slug, and client ID inline and the private key + client
secret as secrets. A minimal setup:

```yaml
config:
  github:
    appId: "123456"
    slug: "acme-spacefleet"
    clientId: "Iv1.0123456789abcdef"
```

### The private key and client secret are secrets

`config.github.privateKey` and `config.github.clientSecret` are credentials —
keep them out of plaintext values and Git history. The private key accepts the
PEM either as-is (multi-line) or base64-encoded (single line, friendlier for
env vars). You have two ways to supply them, exactly like the SMTP password and
the credential-encryption key (see [Secret configuration](secrets.md)):

**Inline (trials).** The chart stores them in the release's own Secret as
`GITHUB_APP_PRIVATE_KEY` and `GITHUB_APP_CLIENT_SECRET`:

```yaml
config:
  github:
    clientSecret: "..."
    privateKey: |
      -----BEGIN RSA PRIVATE KEY-----
      ...
      -----END RSA PRIVATE KEY-----
```

**From a Secret you manage (recommended for production / GitOps).** Put them
in your own Secret under the keys `GITHUB_APP_PRIVATE_KEY` and
`GITHUB_APP_CLIENT_SECRET` and reference it — your values never contain the
secrets:

```sh
kubectl create secret generic spacefleet-app-secrets \
  --from-file=GITHUB_APP_PRIVATE_KEY=path/to/app.private-key.pem \
  --from-literal=GITHUB_APP_CLIENT_SECRET=...
```

```yaml
config:
  secrets:
    envFrom:
      - secretRef:
          name: spacefleet-app-secrets
```

`config.secrets.envFrom` is the same mechanism used for all secret settings, so a
single Secret can carry `GITHUB_APP_PRIVATE_KEY` and `GITHUB_APP_CLIENT_SECRET`
alongside `SPACEFLEET_SECRET_KEY` and the database URL. If you set a value both
inline and via `envFrom`, the inline value wins.

> [!NOTE]
> A configured-but-unparseable private key fails fast — the app and worker won't
> start, with a clear "build github app" error in their logs. A *missing* key
> just leaves the feature off. Both the web and the worker process need the key:
> the web serves the connect flow, and the worker fetches the chart at deploy
> time.

## How an organization connects a repository

Once you've configured the App, the rest is self-service for each organization,
in the app's UI (under **Admin → GitHub**):

1. An organization admin clicks **Connect GitHub** and is sent to GitHub to
   install your App, choosing which repositories to grant. As part of the
   install, GitHub also asks them to authorize the App (the "Request user
   authorization" setting above).
2. GitHub returns them to Spacefleet, which records the installation. The
   handshake is tied both to the organization that started it *and* to the
   GitHub user who completed the install — Spacefleet verifies that user can
   actually access the installation before linking it, so an installation can't
   be claimed by a different organization or by someone passing an installation
   id they don't own.
3. When creating a Git-source application, they select the connected
   installation; deployments from that app can then read the private repository.

Nothing credential-bearing is stored for the installation — at each deploy,
Spacefleet mints a short-lived access token from your App's private key, uses it
for that one fetch, and discards it.

## Leaving it unconfigured

If you don't set `config.github.*`, the GitHub App stays off and nothing breaks:

- The Git chart source still deploys from **public** repositories.
- The app's **Admin → GitHub** screen tells admins no GitHub App is configured,
  so they know the feature isn't available on this deployment.

Configure the App only when you want organizations to deploy from private Git
repositories.

## Verifying it took effect

After installing or upgrading, confirm the variables reached **both** the web
and worker pods:

```sh
kubectl exec deploy/<release>-web    -- printenv GITHUB_APP_ID GITHUB_APP_SLUG GITHUB_APP_CLIENT_ID
kubectl exec deploy/<release>-worker -- printenv GITHUB_APP_ID GITHUB_APP_SLUG GITHUB_APP_CLIENT_ID
```

Then open **Admin → GitHub** in the app: the **Connect GitHub** button appears
only when the App is configured. Click it, install on a test repository, and
confirm the installation is listed afterward. If it doesn't work:

- **Lands on a "not found" page after installing, or the connection fails** —
  the App's **Callback URL** doesn't match `config.externalURL` +
  `/github/callback`. Fix it on the App's GitHub settings page.
- **"Missing installation details from GitHub" / "missing authorization code"
  after installing** — the App's **Request user authorization (OAuth) during
  installation** box isn't checked. Enable it on the App's GitHub settings
  page; without it GitHub doesn't send the code Spacefleet needs to verify the
  installation.
- **"github app is not configured" (the button is missing)** — one of
  `appId`/`slug`/`privateKey`/`clientId`/`clientSecret` is empty, or didn't
  reach the pods. Re-check the five values and the `printenv` above.
- **"does not have access to this installation"** — the code exchange worked
  but the claimed installation isn't one the installing user can access. If
  this happens to a legitimate user, have them restart the connect flow from
  **Admin → GitHub** and complete it in the same browser session; check the
  App's client ID/secret match the App the installation belongs to.
- **"cannot sign the connect flow" / state errors** — `SPACEFLEET_SECRET_KEY`
  isn't set; the connect handshake needs it (see
  [Secret configuration](secrets.md)). An *expired* state just means the user
  took too long — have them click **Connect GitHub** again.
- **A deploy from a private repo fails to fetch the chart** — confirm the App's
  installation actually includes that repository (on GitHub, the org admin can
  add it under the App's installation settings), and that the App has
  **Contents: Read-only**.
- **The pods won't start with a "build github app" error** — the private key is
  malformed. Re-download the `.pem` from GitHub and supply it intact (or
  base64-encode the whole file).

## See also

- [Secret configuration](secrets.md) — the general pattern for supplying secret
  settings (including `GITHUB_APP_PRIVATE_KEY`) from a Secret you manage.
- [Install & configure with Helm](install-with-helm.md) — the full deployment
  walkthrough.
- [Authentication](authentication.md) — connecting GitHub as an upstream **login**
  provider (a separate concern from pulling charts from private repositories).
