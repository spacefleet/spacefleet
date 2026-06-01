---
title: Database configuration
description: Configure Spacefleet's Postgres connection — the connection string, TLS modes, enforcing full certificate validation, and mounting a managed provider's CA bundle such as Amazon RDS.
category: Operator
tags: [postgres, database, tls, ssl, rds, helm, configuration]
---

# Database configuration

Spacefleet stores all of its data in **PostgreSQL**. You can run the bundled
Postgres for a trial, or — recommended for production — point the app at a
managed database such as Amazon RDS, Google Cloud SQL, or Azure Database for
PostgreSQL.

This page covers how to configure that connection: the connection string, how
to require encrypted connections, and how to enforce **full TLS certificate
validation** (including providing a custom CA bundle when your provider needs
one).

For where this fits in a full install, see
[Install & Configure with Helm](install-with-helm.md).

## How the database is configured

The app reads a single Postgres connection string from the `DATABASE_URL`
environment variable. The Helm chart gives you three ways to supply it:

| Mode | Helm values | When to use |
| --- | --- | --- |
| **Bundled** (default) | `postgresql.enabled=true` | Trials and small deployments. Single-replica, not HA. |
| **External, inline URL** | `postgresql.enabled=false`, `externalDatabase.url=…` | Quick external setup; the URL ends up in the release's Secret. |
| **External, existing Secret** | `postgresql.enabled=false`, `externalDatabase.existingSecret=…` | **Recommended for production** — credentials stay in a Secret you control. |

For production, disable the bundled Postgres and point at your managed database.
Keeping the connection string in a Secret you create yourself keeps credentials
out of your Helm values and release history:

```sh
kubectl create secret generic spacefleet-db \
  --from-literal=DATABASE_URL='postgres://user:pass@mydb.example.com:5432/spacefleet?sslmode=verify-full'
```

```yaml
# values.prod.yaml
postgresql:
  enabled: false
externalDatabase:
  existingSecret: spacefleet-db
  # existingSecretKey defaults to DATABASE_URL; override if your Secret uses a
  # different key.
```

The same Secret is consumed by the **web**, **worker**, and **migrate** pods —
all three connect to Postgres with this one connection string.

## The connection string

`DATABASE_URL` is a standard PostgreSQL connection URI:

```
postgres://USER:PASSWORD@HOST:PORT/DATABASE?PARAM=VALUE&…
```

TLS behavior is controlled entirely by query parameters on this URL. The ones
that matter for a secure connection are:

| Parameter | Purpose |
| --- | --- |
| `sslmode` | How strictly TLS is required and validated (see below). |
| `sslrootcert` | Path to a CA bundle (PEM) used to verify the server certificate. If omitted, the system trust store is used. |
| `sslcert` / `sslkey` | Client certificate and key, for databases that require **mutual** TLS (mTLS). |

> Always URL-encode special characters in the username and password (for
> example `@` becomes `%40`), otherwise they will be misparsed as part of the
> host.

## TLS modes

The `sslmode` parameter has the standard libpq meanings. The important
distinction is whether the certificate is actually **validated**:

| `sslmode` | Encrypted? | Validates cert chain? | Validates hostname? |
| --- | --- | --- | --- |
| `disable` | No | — | — |
| `require` | Yes | **No** | No |
| `verify-ca` | Yes | Yes | No |
| `verify-full` | Yes | Yes | **Yes** |

A common misconception is that `sslmode=require` is secure. It encrypts the
connection but performs **no certificate validation at all**, so it does not
protect against an active man-in-the-middle. For any database reachable over a
network you do not fully trust, use **`verify-full`** — it verifies both that
the certificate chains to a trusted CA and that it was issued for the host you
are connecting to.

```
postgres://user:pass@mydb.example.com:5432/spacefleet?sslmode=verify-full
```

## Full validation against publicly-trusted CAs

With `sslmode=verify-full` and **no** `sslrootcert`, the app validates the
server certificate against the operating system's trust store. The Spacefleet
container image ships the standard CA-certificates bundle, so this works out of
the box for any database whose certificate chains to a public root.

**Amazon RDS is the common case here.** Modern RDS instances (using the default
`rds-ca-rsa2048-g1` certificate authority) present a certificate that chains to
publicly-trusted Amazon roots, which are already in the image's trust store. So
for a current RDS instance you usually need **no certificate file at all** —
just require full validation:

```yaml
externalDatabase:
  url: "postgres://user:pass@mydb.abc123.us-east-1.rds.amazonaws.com:5432/spacefleet?sslmode=verify-full"
```

Use the explicit-CA approach in the next section if any of the following apply:

- You are on an **older RDS CA** (e.g. the legacy `rds-ca-2019`) whose root is
  not in public trust stores.
- Your security policy requires **pinning** to your provider's published CA
  rather than trusting public roots.
- Your provider uses a **private CA** (common with on-prem or internal managed
  databases).

## Providing a custom CA bundle (e.g. Amazon RDS)

To validate against a specific CA bundle, you must do two things:

1. Make the CA bundle (a PEM file) available **inside the app's pods**.
2. Point `sslrootcert` at the path you mounted it to.

The chart exposes `config.extraVolumes` and `config.extraVolumeMounts` for
exactly this. They are applied to **all three** database clients — the web,
worker, and migrate pods — so migrations validate the same way the running app
does.

### Step 1 — get the CA bundle into the cluster

Download your provider's CA bundle and store it in a ConfigMap (a CA
certificate is public, not a secret, so a ConfigMap is appropriate). For Amazon
RDS, AWS publishes a global bundle covering all regions:

```sh
curl -fsSLo global-bundle.pem \
  https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem

kubectl create configmap rds-ca-bundle \
  --from-file=global-bundle.pem=global-bundle.pem
```

> Create the ConfigMap in the **same namespace** as the Spacefleet release.

### Step 2 — mount it and point `sslrootcert` at it

```yaml
# values.prod.yaml
postgresql:
  enabled: false

externalDatabase:
  # The mount path below is referenced by sslrootcert.
  url: "postgres://user:pass@mydb.abc123.us-east-1.rds.amazonaws.com:5432/spacefleet?sslmode=verify-full&sslrootcert=/etc/ssl/rds/global-bundle.pem"

config:
  extraVolumes:
    - name: rds-ca
      configMap:
        name: rds-ca-bundle
  extraVolumeMounts:
    - name: rds-ca
      mountPath: /etc/ssl/rds
      readOnly: true
```

The file lands at `/etc/ssl/rds/global-bundle.pem` (the `mountPath` plus the key
you used in the ConfigMap), which is what `sslrootcert` references.

If you keep the connection string in an existing Secret rather than
`externalDatabase.url`, put the same `sslmode`/`sslrootcert` query parameters on
the `DATABASE_URL` value inside that Secret — the volume configuration in
`config.*` is independent of how the URL is supplied.

### Mutual TLS (client certificates)

If your database requires the *client* to present a certificate, mount the
client certificate and key the same way (typically from a Secret rather than a
ConfigMap, since the key is sensitive) and add `sslcert` and `sslkey` to the
connection string:

```
…?sslmode=verify-full&sslrootcert=/etc/ssl/rds/global-bundle.pem&sslcert=/etc/ssl/db-client/tls.crt&sslkey=/etc/ssl/db-client/tls.key
```

## Verifying the connection

After `helm upgrade`, the **migrate** Job runs first. If TLS is misconfigured it
fails there, before the app rolls out. Check it:

```sh
kubectl logs job/spacefleet-migrate
```

Common failures and what they mean:

| Log message | Cause |
| --- | --- |
| `x509: certificate signed by unknown authority` | The CA that signed the server cert is not trusted — wrong or missing `sslrootcert`, or the bundle does not cover this server. |
| `certificate is valid for X, not Y` | Hostname mismatch under `verify-full` — connect using the hostname the certificate was issued for, not an IP or alias. |
| `no such file or directory` referencing your `sslrootcert` path | The CA file is not mounted where the URL expects it — check that `mountPath` + the ConfigMap key equal the `sslrootcert` path. |
| `connection refused` / timeouts | Not TLS — a network/security-group/firewall issue reaching the database host. |

Once migrations succeed, the web and worker pods use the identical connection
string and TLS assets, so a green migrate Job means the app will connect too.
