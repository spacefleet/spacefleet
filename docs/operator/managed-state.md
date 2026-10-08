---
title: Managed OpenTofu state
description: Operate Spacefleet's managed OpenTofu state backend — what it needs (the encryption key, runner connectivity), state size and ingress body limits, backups, and troubleshooting.
category: Operator
tags: [opentofu, terraform, state, backups, runners, helm, configuration]
---

# Managed OpenTofu state

OpenTofu components can keep their state in Spacefleet itself: the
**Spacefleet (managed)** state backend, which is the default for new OpenTofu
components. Users get state with nothing to set up — no bucket, no cloud
credential, no lock table — and Spacefleet keeps every version of it, encrypted,
and locks it during runs.

That convenience moves a responsibility to you: **the state now lives in
Spacefleet's database.** This page covers what the feature needs, how runner
clusters reach it, and how to protect the data.

For how users work with it, see
[Deploying with workflows](../user/deploy-workflows.md#state-backend).

## What it needs

1. **The credential-encryption key** (`SPACEFLEET_SECRET_KEY`). State is
   encrypted with it before it is stored, and each run's access to its state
   is signed with a key derived from it. Without the key, the editor doesn't
   offer the managed backend and saving a component that uses it is refused.
   See [Secret configuration](secrets.md). The key's usual warning applies
   with extra force here: changing it makes every stored state version
   unreadable.
2. **A path from runner pods back to Spacefleet.** An OpenTofu step on
   managed state reads, locks, and writes its state over HTTP from the runner
   pod. This is the only time a runner calls back to Spacefleet — see
   [Connectivity](#connectivity).
3. **A database you back up.** See [Backups](#backups).

## Connectivity

Each OpenTofu step is given the address of its state and a short-lived token
that is valid only while that step is running. Where the address points
depends on how the runner cluster is registered:

| Runner registration | Address the step uses |
| --- | --- |
| **In-cluster** (the runner is the cluster Spacefleet runs in) | This release's Service, inside the cluster — no ingress, no body-size limit |
| Any other method (token, kubeconfig, EKS, GKE, AKS) | `config.runnerAPIURL`, which defaults to `config.externalURL` |

So for a remote runner cluster, its pods must be able to reach
`config.externalURL` (or the URL you set in `config.runnerAPIURL`) over the
network. Set `config.runnerAPIURL` when runners reach the app at a different
address than browsers do — an internal load balancer, a private DNS name:

```yaml
config:
  externalURL: https://spacefleet.example.com
  runnerAPIURL: https://spacefleet.internal.example.com
```

The state routes live under `/api/tofu/state/` on the same host as the app.
They authenticate with the per-step token, not with a user's sign-in, so they
must not sit behind an extra authentication layer (an OAuth proxy or SSO
gateway) in front of the app. They use only `GET`, `POST`, and `DELETE`.

The address must use a certificate the runner pod trusts (a public CA).
Certificates from a private CA aren't supported for this connection yet; use
an in-cluster runner or a publicly trusted certificate.

## State size and ingress limits

Spacefleet accepts a state upload of up to 64 MiB by default. Change it with
`config.tofuStateMaxBytes` (the `TOFU_STATE_MAX_BYTES` environment variable):

```yaml
config:
  tofuStateMaxBytes: 134217728   # 128 MiB
```

Your ingress has its own limit, and it is usually much lower. ingress-nginx
rejects request bodies over **1 MB** by default, which a real state passes
quickly. Raise it for the app's ingress:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: 64m
```

A step whose state is too large fails at the end of its apply with an HTTP
`413` error in its log. Runners registered in-cluster bypass the ingress
entirely.

## Backups

Managed state is ordinary rows in Spacefleet's Postgres database: every version
of every component's state, encrypted. **If the database is lost, the state is
lost**, and OpenTofu no longer knows about the infrastructure it created.

- Run production on an **external, managed database** with automated backups
  and point-in-time recovery — see [Database configuration](database.md). The
  bundled Postgres is for trials.
- Back up the **encryption key** separately (your secrets manager). A database
  backup without its key cannot be decrypted.
- Every version is kept for now; plan database storage for it. State is
  compressed before it is stored.

State outlives the component it belongs to: removing a component from a
workflow keeps its state (it still describes real infrastructure). It is
deleted with its application.

## Troubleshooting

Look at the failed step's log in the run view. OpenTofu reports the HTTP status
the state request got:

| What the log shows | Cause | Fix |
| --- | --- | --- |
| `connection refused`, `no such host`, or a timeout during `tofu init` | The runner pod can't reach the state address | Check `config.runnerAPIURL` (or `config.externalURL`) from inside the runner cluster, e.g. `kubectl run -it --rm curl --image=curlimages/curl -- curl -sI <url>/api/health` |
| `x509: certificate signed by unknown authority` | The address uses a certificate the pod doesn't trust | Use a publicly trusted certificate, or an in-cluster runner |
| `HTTP remote state endpoint requires auth` or `HTTP error: 401` | The step's token was refused: the step is no longer running (the run was cancelled or timed out) | Start a new run |
| `HTTP error: 413` | The state is larger than an upload limit | Raise `config.tofuStateMaxBytes` and the ingress body limit (above) |
| `internal server error` or `HTTP error: 500` | Spacefleet couldn't read or store the state | Check the web pods' logs for lines starting `tofu state:` |
| `Error acquiring the state lock` | Another step holds the lock, or a step died holding it | See *State operations → Release a stuck state lock* in [Deploying with workflows](../user/deploy-workflows.md#state-operations) |
| "managed state is not configured" | No encryption key is set | Set `SPACEFLEET_SECRET_KEY` — see [Secret configuration](secrets.md) |

## See also

- [Deploying with workflows](../user/deploy-workflows.md) — OpenTofu
  components and their state backends, from the user's side.
- [Secret configuration](secrets.md) — the encryption key managed state needs.
- [Database configuration](database.md) — running on an external database.
- [Install & Configure with Helm](install-with-helm.md) — the full install.
