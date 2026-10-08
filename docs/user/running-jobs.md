# Running jobs in a cluster

Spacefleet can run jobs — CI/CD steps, Helm releases, and other one-off or
pipeline work — inside the Kubernetes clusters you've registered. Job running is
powered by [Tekton](https://tekton.dev/), which Spacefleet installs and manages
for you.

## Designate a cluster to run jobs

1. Open **Providers › Clusters** and find the cluster you want to use.
2. Select **Jobs** on that cluster's row.
3. Select **Enable job running**.

If the cluster doesn't already have Tekton, Spacefleet installs it for you. The
panel shows live progress as the install proceeds — you don't need to refresh.
When it finishes, the status shows **installed** and the controller as ready.

Jobs run in a dedicated `spacefleet-jobs` namespace on the cluster, keeping
Spacefleet's runs (and the secrets that support them) in one place, out of the
`default` namespace. Spacefleet creates that namespace when it installs,
upgrades, or syncs Tekton — an install set up before this namespace was
introduced shows **Update available** in the Jobs panel (see below). If you
installed Tekton yourself, create it once with
`kubectl create namespace spacefleet-jobs` — runs fail until it exists.

Enabling job running requires credentials that can install cluster-wide
components (custom resource definitions, RBAC, and webhooks) — effectively
cluster-admin. If the install fails with a permissions error, the status shows
**failed** with the reason; grant the cluster's credentials the needed access
and enable again.

## Keep the managed install up to date

The Jobs panel's **Engine** section shows the Tekton install Spacefleet manages
on the cluster: its version, that it's managed by Spacefleet, and whether it
matches what your version of Spacefleet sets up. When it doesn't — a newer
Tekton version is available, or Spacefleet's install has changed (for example,
it now includes the jobs namespace or additional roles) — the panel shows
**Update available** with one of two actions:

- **Upgrade to (version)** — a newer Tekton version is available.
- **Sync install** — the Tekton version is current, but the installed setup
  differs from what Spacefleet expects.

Both re-apply the full managed install and show live progress, like the
initial install. Updating requires the same cluster-admin-level credentials as
installing.

These actions only appear for a Tekton that Spacefleet installed. A Tekton you
installed yourself is never modified — keeping it current is up to you.

## Speed up OpenTofu runs with a provider cache

Every OpenTofu run starts with `tofu init`, which downloads the module's
providers — often hundreds of megabytes for the big cloud providers — into a
fresh, throwaway workspace. A **provider plugin cache** keeps them on the
cluster instead: a shared volume in the `spacefleet-jobs` namespace that every
OpenTofu step on this runner mounts as its plugin cache, so a provider is
downloaded once per cluster and linked into each run afterwards.

Turn it on from the Jobs panel's **Engine** section, under **Provider plugin
cache**: choose a size (`20Gi` is plenty for most stacks — a provider is kept
once per version) and, if the cluster's default storage class cannot be shared
between nodes, a storage class that supports **ReadWriteMany** (for example an
NFS- or file-store-backed class). The volume is created right away; OpenTofu
steps start using it on their next run. The cache can **grow** in place
(enter a larger size and press **Resize**) when its storage class allows
volume expansion; to make it smaller or move it to another storage class,
remove the cache and set it up again — a claim cannot shrink or change
class.

Two things to know:

- Steps on **every node** of the cluster mount the same volume, so the storage
  class must allow that. With a class that only supports a single node, steps
  scheduled elsewhere wait forever for the volume.
- OpenTofu's plugin cache is not designed for two `init` commands writing the
  same provider at the very same moment. In practice concurrent runs share a
  cache fine once a provider is present; if you ever see a corrupted provider,
  remove the cache and set it up again.

## Check readiness

The Jobs panel includes a **Readiness** report showing whether the cluster's
credentials are allowed to run jobs (submit and read Tekton runs). If anything
is missing, select the capability and use **Generate RBAC** to get a manifest
you can apply to grant it. (This is the same capability report available from the
cluster's **Capabilities** view.)

## Run a job

Once Tekton is installed, select **Run a job** from the Jobs panel. Provide:

- **Name** — a label for the run.
- **Image** — the container image the step runs in (for example,
  `alpine:3.20`).
- **Script** — the shell script to execute.

Select **Run job**. The run's status updates live (Pending → Running →
Succeeded / Failed), and its log output streams in as it runs.

## Stop designating a cluster

Select **Disable** in the Jobs panel to stop using a cluster for jobs. This
clears the designation only — it does not uninstall Tekton from the cluster.
