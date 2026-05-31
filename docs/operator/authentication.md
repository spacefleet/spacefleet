---
title: Authentication
description: Connect Spacefleet to your identity provider with OpenID Connect (OIDC) — what to register, the two settings to configure, and how to verify sign-in works.
category: Operator
tags: [authentication, oidc, sso, identity-provider, configuration, security]
---

# Authentication

Spacefleet does not manage passwords. Instead, it delegates sign-in to **your
identity provider** using **OpenID Connect (OIDC)** — the same standard behind
"Log in with Google/Okta/Entra." You point Spacefleet at a provider you already
run (or a hosted one), users log in there, and Spacefleet trusts the result.

Any standards-compliant OIDC provider works — Okta, Microsoft Entra ID, Auth0,
Google, Keycloak, Dex, and others. There is no provider-specific setup inside
Spacefleet; you only supply two values (an issuer URL and a client ID) and
register Spacefleet as an application with your provider.

> **Until you configure a provider, Spacefleet runs with authentication turned
> off** and treats every visitor as the same signed-in user. That makes a fresh
> install usable in seconds, but it is **not safe to expose**. See
> [Running without authentication](#running-without-authentication) before
> putting Spacefleet anywhere reachable.

## How sign-in works

From a user's point of view, signing in to Spacefleet looks like any other
single sign-on app:

1. They open Spacefleet and are redirected to your identity provider's login
   page.
2. They authenticate there (password, MFA, passkey — whatever your provider
   enforces).
3. The provider sends them back to Spacefleet, already signed in.

Spacefleet never sees their password. It receives a signed token from your
provider proving who the user is, and checks that token on every request. When
the token expires, the browser renews it silently — users stay logged in
without re-entering credentials until your provider's session ends.

Because identity lives entirely in your provider, **you manage who can sign in
where you already manage everyone else**: disabling a user or enforcing MFA in
your provider immediately applies to Spacefleet.

## What to configure

Spacefleet needs two settings, **neither of which is a secret** — both are sent
to the browser so it can start the login flow:

| Setting | What it is | Example |
| --- | --- | --- |
| **Issuer URL** | Your provider's OIDC base URL. Spacefleet looks up the rest of the configuration automatically at `<issuer>/.well-known/openid-configuration`. Setting this is what turns real authentication **on**. | `https://login.example.com` |
| **Client ID** | The identifier your provider assigns to Spacefleet when you register it as an application. | `spacefleet` |

Where you set these depends on how you run Spacefleet:

- **On Kubernetes (Helm):** `config.oidc.issuer` and `config.oidc.clientID` —
  see [Install & configure with Helm](install-with-helm.md#1-configure-authentication-oidc).
- **Running the binary directly:** the `OIDC_ISSUER` and `OIDC_CLIENT_ID`
  environment variables.

## Connecting your identity provider

The exact screens differ per provider, but the steps are the same everywhere:

1. **Register Spacefleet as an application** in your provider, choosing a
   **public / single-page-app client** that uses **Authorization Code flow with
   PKCE**. Do not create a "confidential" client with a secret — Spacefleet's
   frontend runs in the browser and cannot hold a secret.

2. **Add the redirect (callback) URL.** This is your Spacefleet address with
   `/auth/callback` appended, for example:

   ```
   https://spacefleet.example.com/auth/callback
   ```

   Add it to the client's list of allowed redirect URIs.

3. **Allow Spacefleet's web address as an origin.** The browser calls your
   provider directly during login, so your provider's CORS / "allowed origins"
   list must include Spacefleet's address (e.g.
   `https://spacefleet.example.com`).

4. **Copy the issuer URL and client ID** from your provider and set them on
   Spacefleet (see [What to configure](#what-to-configure)). The issuer must be
   reachable both from users' browsers and from the Spacefleet server itself.

5. **Restart Spacefleet** (or roll out the Helm change) and sign in. On first
   login Spacefleet creates a local record for the user automatically — there is
   no separate user-import step.

That's the whole integration. If your provider issues tokens whose audience
differs from the client ID you registered, set them to match — Spacefleet
requires the token audience to equal the configured client ID.

## Running without authentication

If you do not set an issuer URL, Spacefleet starts with authentication **turned
off**: every request is treated as the same built-in user, with no login screen
and no verification of any kind.

This exists so you can try Spacefleet immediately, before wiring up a provider.
Spacefleet makes the state obvious so you don't ship it by accident:

- it logs a warning at startup, and
- the Helm chart prints a warning after install while no issuer is configured.

> ⚠️ **Never expose Spacefleet with authentication off.** In this mode anyone who
> can reach the app has full access — there is no security boundary at all. Set
> an issuer URL before making Spacefleet reachable by anyone but you.

## Troubleshooting

**Every request fails with "unauthorized" right after enabling OIDC.** The token
audience doesn't match your client ID. Make sure the client ID you configured on
Spacefleet is the same one you registered with your provider, and that your
provider issues tokens with that client ID as the audience.

**Spacefleet won't start and logs a discovery error.** It can't reach the issuer
URL. From the Spacefleet server, confirm that
`<issuer>/.well-known/openid-configuration` loads. A common cause is an issuer
that's only reachable from inside a different network, or a typo in the URL.
Spacefleet retries a few times at startup before giving up.

**Login bounces back to the provider in a loop, or the provider rejects the
redirect.** The callback URL isn't registered, or Spacefleet's origin isn't in
the provider's allowed origins. Add both exactly as your users reach Spacefleet
(scheme, host, and `/auth/callback` path).

**The browser console shows CORS errors talking to the provider during login.**
Spacefleet's web address isn't in your provider's allowed-origins list. Add it
and retry.

**After logging in, users land on a "Not found" page.** This usually means the
app was reached over a different address than the one registered as the redirect
URL (for example `http://` vs `https://`, or a bare IP vs the hostname). Use a
single, consistent address everywhere.

## See also

- [Install & configure with Helm](install-with-helm.md#1-configure-authentication-oidc)
  — where to set the issuer and client ID for a Kubernetes deployment.
