---
title: Secret configuration
description: Configure Spacefleet's credential-encryption key and other secret settings for a Helm deployment — supplying secrets inline for a trial or, for production, from a Secret you manage with Vault, External Secrets Operator, or Sealed Secrets.
category: Operator
tags: [secrets, encryption, helm, vault, configuration, security]
---

# Secret configuration

Some of Spacefleet's settings are **secrets** — values that must not sit in
plaintext in your Helm values, your Git history, or a ConfigMap. The most
important one is the **credential-encryption key**, which Spacefleet uses to
encrypt credentials it stores (for example, a registered Kubernetes cluster's
service-account token or kubeconfig) so they are never written to the database
in the clear.

This page covers how to supply that key, and any other secret settings, to a
Helm deployment.

For where this fits in a full install, see
[Install & Configure with Helm](install-with-helm.md). For the database
connection string (also a secret), see [Database configuration](database.md).

## The credential-encryption key

Spacefleet reads its encryption key from the `SPACEFLEET_SECRET_KEY` environment
variable. It must be a **base64-encoded 32-byte key**. Generate one with:

```sh
openssl rand -base64 32
```

What happens without it:

- Features that store **no** credentials keep working normally.
- Registering anything that **carries** a credential (a cluster token or
  kubeconfig) **fails immediately** with a clear error telling you to set the
  key — Spacefleet will never fall back to storing a credential unencrypted.

So if you intend to register clusters with credentials, set the key before you
start.

> [!WARNING]
> **The key cannot be rotated in place.** Everything Spacefleet encrypts is
> encrypted with the key that was set at the time. If you change the key, every
> value already encrypted with the old one becomes unreadable. Set it **once**,
> store a backup somewhere safe (your secrets manager), and keep it stable for
> the life of the data.

## How to supply secrets

The Helm chart gives you two ways to provide secret settings. They can be
combined, and both apply to the web and worker pods.

| Mode | Helm values | When to use |
| --- | --- | --- |
| **Inline** | `config.secrets.secretKey=…` | Trials and quick setups. The chart stores the value in a Secret it creates for the release. |
| **From a Secret you manage** | `config.secrets.envFrom=[…]` | **Recommended for production / GitOps.** You create and control the Secret; the chart only references it. |

If both supply the same variable, the inline value wins.

### Inline (trial)

Set the key directly in your values. The chart writes it into the release's own
Secret and hands it to the pods:

```yaml
config:
  secrets:
    secretKey: "PASTE_OUTPUT_OF_openssl_rand_-base64_32"
```

This is convenient, but the key lives in your values file (and therefore your
release history). For anything beyond a trial, prefer the next option.

### From a Secret you manage (recommended)

Create a Kubernetes Secret yourself — by hand, or via a tool like **HashiCorp
Vault**, the **External Secrets Operator**, or **Sealed Secrets** — and have the
chart load it as environment variables. Your values never contain the secret.

```sh
kubectl create secret generic spacefleet-app-secrets \
  --from-literal=SPACEFLEET_SECRET_KEY="$(openssl rand -base64 32)"
```

```yaml
config:
  secrets:
    envFrom:
      - secretRef:
          name: spacefleet-app-secrets
```

Every key in the referenced Secret becomes an environment variable on the web
and worker pods. This is the **scalable path for all secret configuration**: as
Spacefleet (or your own integrations) gain new secret settings, add them as keys
to your Secret — no chart change and no new values needed.

`config.secrets.envFrom` accepts the standard Kubernetes `envFrom` list, so you
can reference multiple Secrets, mix in a ConfigMap, or add a prefix:

```yaml
config:
  secrets:
    envFrom:
      - secretRef:
          name: spacefleet-app-secrets
      - secretRef:
          name: spacefleet-extra-secrets
```

## Verifying it took effect

After installing or upgrading, confirm the variable is present on the pods:

```sh
kubectl exec deploy/<release>-web -- printenv SPACEFLEET_SECRET_KEY
```

If you registered a cluster credential and got an error about a missing
encryption key, the variable isn't reaching the pod — check that your Secret
exists in the same namespace and that its key is spelled exactly
`SPACEFLEET_SECRET_KEY`.
