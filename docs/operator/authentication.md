---
title: Authentication
description: How Spacefleet authenticates users with OpenID Connect (OIDC) — the sign-in flow, the three ways to provide a login (bundled Dex, your own provider, or none), choosing between them, and how to verify it works.
category: Operator
tags: [authentication, oidc, sso, identity-provider, configuration, security]
---

# Authentication

Spacefleet does not manage passwords. It delegates sign-in to an **OpenID
Connect (OIDC)** provider — the same standard behind "Log in with
Google/Okta/Entra." A provider handles the login; Spacefleet trusts the signed
result and checks it on every request.

This page explains how that works and the three ways to set it up. If you just
want the bundled, batteries-included option, jump to
[Authenticate with the bundled Dex](authentication-with-dex.md).

> **A fresh install ships with authentication turned _off_** (every visitor is
> the same built-in user) so you can try Spacefleet in seconds. That is **not
> safe to expose** — see [Running without authentication](#running-without-authentication).

## How sign-in works

From a user's point of view, signing in to Spacefleet looks like any other
single sign-on app:

1. They open Spacefleet and are redirected to the provider's login page.
2. They authenticate there — password, MFA, passkey, "Log in with GitHub",
   whatever the provider enforces.
3. The provider sends them back to Spacefleet, already signed in.

Spacefleet never sees their password. It receives a **signed token** proving who
the user is, verifies that token's signature and claims on every request, and
keeps the session alive by renewing the token silently until the provider's
session ends.

Two consequences worth knowing:

- **Identity lives in the provider.** Disabling a user, requiring MFA, or
  changing group membership in the provider applies to Spacefleet immediately —
  you manage access where you already manage everyone else.
- **Accounts are created on first login.** The first time someone signs in,
  Spacefleet creates a local record for them automatically from the provider's
  token (their stable user ID and email). There is no separate user-import step.

### What Spacefleet needs

Whatever provider you use, Spacefleet needs just two settings, **neither of which
is a secret** (both are sent to the browser so it can start the login flow):

| Setting | What it is | Example |
| --- | --- | --- |
| **Issuer URL** | The provider's OIDC base URL. Spacefleet discovers everything else from `<issuer>/.well-known/openid-configuration`. Setting this is what turns real authentication **on**. | `https://login.example.com` |
| **Client ID** | The identifier the provider assigns to Spacefleet when you register it as an application. | `spacefleet` |

Where you set them depends on how you run Spacefleet:

- **On Kubernetes (Helm):** `config.oidc.issuer` and `config.oidc.clientID` (or,
  with the bundled Dex, the chart fills the issuer in for you — see below).
- **Running the binary directly:** the `OIDC_ISSUER` and `OIDC_CLIENT_ID`
  environment variables.

Spacefleet registers as a **public client** using the Authorization Code flow
with **PKCE** — its frontend runs in the browser and holds no client secret.

## Three ways to provide a login

| Approach | Best for | What you run |
| --- | --- | --- |
| **Bundled Dex** | Most deployments — you want real auth without operating a separate identity system. | Nothing extra; the chart deploys Dex for you. |
| **Your own provider** | You already run or pay for an IdP (Okta, Entra ID, Auth0, Google, Keycloak, an existing Dex…). | Your existing provider. |
| **None (dev passthrough)** | A quick local trial, never exposed. | Nothing — and no security. |

**Bundled Dex** is the recommended default. The Helm chart can deploy
[Dex](https://dexidp.io/) — a small, self-contained OIDC provider — alongside
the app. It can authenticate against logins your users already have (GitHub,
Google, LDAP, another OIDC provider) or its own built-in accounts, and it's
served on the same address as the app so there's no cross-origin setup. This has
its own page: [Authenticate with the bundled Dex](authentication-with-dex.md).

**Your own provider** is covered next. **None** is
[the dev passthrough](#running-without-authentication).

## Connect your own identity provider

Use this if you already have an OIDC provider and would rather point Spacefleet
at it than run the bundled Dex. The exact screens differ per provider, but the
steps are the same everywhere:

1. **Register Spacefleet as an application** in your provider, choosing a
   **public / single-page-app client** that uses **Authorization Code flow with
   PKCE**. Do not create a "confidential" client with a secret — Spacefleet's
   frontend runs in the browser and cannot hold one.

2. **Add the redirect (callback) URL** — your Spacefleet address with
   `/auth/callback` appended — to the client's allowed redirect URIs:

   ```
   https://spacefleet.example.com/auth/callback
   ```

3. **Allow Spacefleet's web address as an origin.** The browser calls the
   provider directly during login, so the provider's CORS / "allowed origins"
   list must include Spacefleet's address (e.g. `https://spacefleet.example.com`).

4. **Copy the issuer URL and client ID** and set them on Spacefleet (see
   [What Spacefleet needs](#what-spacefleet-needs)). The issuer must be reachable
   **both** from users' browsers and from the Spacefleet server itself.

5. **Roll out the change and sign in.** On first login Spacefleet provisions the
   user automatically.

If your provider issues tokens whose audience differs from the client ID you
registered, set them to match — Spacefleet requires the token audience to equal
the configured client ID.

## Running without authentication

If no issuer is configured (and the bundled Dex is off), Spacefleet starts with
authentication **turned off**: every request is treated as the same built-in
user, with no login screen and no verification of any kind.

This exists so you can try Spacefleet immediately. Spacefleet makes the state
obvious so you don't ship it by accident — it logs a warning at startup, and the
Helm chart prints a warning after install while no provider is configured.

> ⚠️ **Never expose Spacefleet with authentication off.** In this mode anyone who
> can reach the app has full access — there is no security boundary at all.
> Before making Spacefleet reachable by anyone but you, either enable the
> [bundled Dex](authentication-with-dex.md) or
> [connect your own provider](#connect-your-own-identity-provider).

## Troubleshooting

These cover any provider. For issues specific to the bundled Dex, see its
[troubleshooting section](authentication-with-dex.md#troubleshooting).

**Every request fails with "unauthorized" right after enabling OIDC.** The token
audience doesn't match your client ID. Make sure the client ID configured on
Spacefleet is the same one registered with your provider, and that the provider
issues tokens with that client ID as the audience.

**Spacefleet won't start and logs a discovery error.** It can't reach the issuer
URL. From the Spacefleet server, confirm that
`<issuer>/.well-known/openid-configuration` loads. A common cause is an issuer
only reachable from a different network, or a typo in the URL. Spacefleet retries
a few times at startup before giving up.

**Login bounces back to the provider in a loop, or the provider rejects the
redirect.** The callback URL isn't registered, or Spacefleet's origin isn't in
the provider's allowed origins. Add both exactly as your users reach Spacefleet
(scheme, host, and the `/auth/callback` path).

**The browser console shows CORS errors talking to the provider during login.**
Spacefleet's web address isn't in the provider's allowed-origins list. Add it and
retry. (The bundled Dex avoids this by being same-origin with the app.)

**After logging in, users land on a "Not found" page.** The app was reached over
a different address than the one registered as the redirect URL (for example
`http://` vs `https://`, or a bare IP vs the hostname). Use a single, consistent
address everywhere.

## See also

- [Authenticate with the bundled Dex](authentication-with-dex.md) — enable Dex,
  add GitHub/Google logins, choose storage, and harden it for production.
- [Install & configure with Helm](install-with-helm.md#1-configure-authentication-oidc)
  — where authentication fits in a Kubernetes deployment.
