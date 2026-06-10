---
title: Email
description: Configure Spacefleet's outbound email over SMTP for a Helm deployment — the SMTP settings, supplying the password safely, what happens when email is left unconfigured, and verifying delivery. Covers the environment variables and Helm values, not how to use the features that send mail.
category: Operator
tags: [email, smtp, helm, configuration, invitations, notifications]
---

# Email

Spacefleet can send outbound email over **SMTP** — today, organization
**invitation** messages. Email is **optional**: when it isn't configured,
Spacefleet still works and every invitation still produces a copy-able link an
admin can share by hand. Configuring SMTP simply lets Spacefleet deliver those
links by email instead.

This page covers the operator side: which settings to provide, how to supply the
password safely, and how to confirm delivery. It does not cover *using* the
features that send mail — that's in the user documentation.

For where this fits in a full install, see
[Install & configure with Helm](install-with-helm.md). The invitation feature
itself is gated by who can administer an organization — see
[Authentication](authentication.md#control-who-can-create-organizations).

## What you need

Point Spacefleet at any SMTP server you already use for transactional mail — your
provider (e.g. Amazon SES, SendGrid, Postmark, Mailgun), a corporate relay, or a
self-hosted server. You'll need:

- the server **hostname** and **port** (commonly `587` for submission with
  STARTTLS),
- a **From address** to send as,
- and, if the server requires authentication, a **username** and **password**.

## Settings

Spacefleet reads the following, set by the Helm chart under `config.smtp`:

| Helm value | Environment variable | Default | Purpose |
| --- | --- | --- | --- |
| `config.smtp.host` | `SMTP_HOST` | _(empty)_ | SMTP server hostname. **Empty disables email.** |
| `config.smtp.port` | `SMTP_PORT` | `587` | SMTP server port. |
| `config.smtp.from` | `SMTP_FROM` | _(empty)_ | Envelope/From address. **Empty disables email.** |
| `config.smtp.username` | `SMTP_USERNAME` | _(empty)_ | Auth username. Omit for an unauthenticated relay. |
| `config.smtp.password` | `SMTP_PASSWORD` | _(empty)_ | Auth password — **a secret**, see below. |
| `config.smtp.startTLS` | `SMTP_STARTTLS` | `true` | Upgrade the connection with STARTTLS after connecting (the usual port-587 setup). When enabled, a server that doesn't offer STARTTLS is a send error — mail is never downgraded to cleartext. |

**Email is enabled only when both `host` and `from` are set.** With either
empty, Spacefleet treats email as off.

> [!IMPORTANT]
> External links in emails (the invitation link) are built from
> **`config.externalURL`**, your deployment's public base URL — not from the SMTP
> settings. If invitation links point at the wrong address, fix `config.externalURL`
> (see [Authentication](authentication.md#sign-in-for-the-first-time)), not your
> mail server.

## Configure it

Provide the non-secret settings inline and the password as a secret. A minimal
authenticated setup:

```yaml
config:
  smtp:
    host: smtp.example.com
    port: 587
    from: "Spacefleet <no-reply@example.com>"
    username: spacefleet
    startTLS: true
```

### The password is a secret

`config.smtp.password` is a credential — keep it out of plaintext values and Git
history. You have two ways to supply it, exactly like the credential-encryption
key (see [Secret configuration](secrets.md)):

**Inline (trials).** The chart stores it in the release's own Secret as
`SMTP_PASSWORD`:

```yaml
config:
  smtp:
    password: "an-app-password"
```

**From a Secret you manage (recommended for production / GitOps).** Put the
password in your own Secret under the key `SMTP_PASSWORD` and reference it — your
values never contain the secret:

```sh
kubectl create secret generic spacefleet-app-secrets \
  --from-literal=SMTP_PASSWORD="an-app-password"
```

```yaml
config:
  secrets:
    envFrom:
      - secretRef:
          name: spacefleet-app-secrets
```

`config.secrets.envFrom` is the same mechanism used for all secret settings, so a
single Secret can carry `SMTP_PASSWORD` alongside `SPACEFLEET_SECRET_KEY` and the
database URL. If you set the password both inline and via `envFrom`, the inline
value wins.

## Leaving email unconfigured

If you don't set `config.smtp.host`/`from`, email stays off and nothing breaks:

- Invitations still work — creating one returns a **copy-able invite link** that
  an admin shares manually (Slack, their own email, etc.).
- The app's invite screen tells admins that email isn't configured, so they know
  to copy the link rather than wait for a message that won't arrive.

This is a perfectly valid way to run Spacefleet; configure SMTP only when you
want delivery automated.

## Verifying it took effect

After installing or upgrading, confirm the variables reached the pods (the
worker process is what actually sends mail):

```sh
kubectl exec deploy/<release>-worker -- printenv SMTP_HOST SMTP_FROM
```

Then exercise it: have an admin invite a test address and check that the message
arrives. If it doesn't:

- **Check the worker logs** (`kubectl logs deploy/<release>-worker`) for SMTP
  errors — authentication failures, connection refused, or TLS problems.
- **Confirm `host` and `from` are both set** — with either empty, email is off
  and the invite only produces a link (no send is attempted).
- **Check the password reached the pod** — `kubectl exec deploy/<release>-worker
  -- printenv SMTP_PASSWORD` (or that your `envFrom` Secret exists in the same
  namespace with the key spelled exactly `SMTP_PASSWORD`).
- **Match TLS to your server** — most submission servers want
  `config.smtp.startTLS=true` on port `587`. With it enabled, a server that
  doesn't offer STARTTLS fails the send (look for "does not advertise STARTTLS"
  in the worker logs) rather than falling back to cleartext; set it to `false`
  only for a server with no TLS at all (e.g. a local test catcher).

## See also

- [Authentication](authentication.md) — who can log in, and who can create or be
  invited to organizations.
- [Secret configuration](secrets.md) — the general pattern for supplying secret
  settings (including `SMTP_PASSWORD`) from a Secret you manage.
- [Install & configure with Helm](install-with-helm.md) — the full deployment
  walkthrough.
