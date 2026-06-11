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
`default` namespace. Spacefleet creates that namespace when it installs or
upgrades Tekton. If you installed Tekton yourself (or enabled job running
before this namespace was introduced), create it once with
`kubectl create namespace spacefleet-jobs` — runs fail until it exists.

Enabling job running requires credentials that can install cluster-wide
components (custom resource definitions, RBAC, and webhooks) — effectively
cluster-admin. If the install fails with a permissions error, the status shows
**failed** with the reason; grant the cluster's credentials the needed access
and enable again.

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
