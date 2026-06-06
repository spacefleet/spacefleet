# Importing existing Helm releases

If you already have Helm releases running on a cluster — installed with the
`helm` CLI or by another tool — you can adopt them into Spacefleet as managed
**applications** without redeploying them. Spacefleet reads the release's
current state from the cluster, pre-fills an application from it, and lets you
fill in the few details Helm doesn't record (where the chart comes from) before
adopting it.

Adopting a release **does not redeploy it**. The release keeps running exactly
as it is; Spacefleet just starts tracking it so you can upgrade, diff, and
uninstall it from the app's page later.

## Discover the releases on a cluster

1. Open **Applications** and select **Create app › Import existing release**.
2. Choose the **cluster** to scan. Optionally enter a **namespace** to limit the
   search; leave it blank to scan all namespaces.
3. Select **Discover**.

Spacefleet lists the Helm releases found on that cluster — their name, namespace,
chart, and status (only the current revision of each release is shown).

If discovery reports a permissions error, the cluster's stored credentials need
permission to **read (list) Secrets** in the namespaces you're scanning — that's
where Helm keeps its release state. Scanning a **single namespace** only needs
that permission within that namespace, but scanning **all namespaces** (leaving
the namespace blank) requires **cluster-wide** permission to list Secrets across
every namespace; a credential scoped to one namespace will only ever find
releases there. Discovery works with Helm's default release storage; releases
stored another way (for example with a ConfigMap or SQL backend) won't appear.

## Adopt a release

1. In the results, select **Import** on the release you want to manage.
2. Spacefleet opens the application form, pre-filled from the live release:
   - **Name**, **release name**, **target namespace**, and **target cluster** are
     taken from the release. The release name, namespace, and cluster are locked
     — they identify the live release.
   - The **values** are the release's current user-supplied values, pulled live
     from the cluster. **Review them before importing** — values passed at
     install time can contain secrets, and they're stored with the application.
   - The **chart name** and **version** are pre-filled where known.
3. Tell Spacefleet **where the chart comes from** — this is the part Helm doesn't
   record. Pick the chart source and complete its fields:
   - an **HTTP Helm repository** (repository URL + chart name),
   - an **OCI registry** reference, or
   - a **Git repository** the chart lives in (and, optionally, values files
     pulled from a Git repository).

   For a private chart or repository, attach a chart credential or GitHub App
   installation as you would for any application.
4. Choose a **runner cluster** — a job-running (Tekton-enabled) cluster that will
   perform future upgrades and diffs. (See
   [Running jobs in a cluster](running-jobs.md).)
5. Select **Import release**.

The application is created in the **deployed** state — no rollout runs.

## Confirm the chart source matches

If a job runner is available, Spacefleet **refreshes** the application
automatically right after importing: it runs a `helm diff` comparing the chart
source and values you configured against what's actually running on the cluster.

A refresh runs as a job on the runner cluster you chose, so it only happens when
a job runner is configured and reachable. **If no job runner is available**, the
import still succeeds but the comparison doesn't run — the application's sync
status stays **unknown** until you run a refresh. Once a runner is configured,
select **Refresh** on the application's page to run the comparison.

When the comparison runs, it reports one of:

- **In sync** — the source you configured reproduces the live release, so you're
  ready to manage it (an upgrade will change only what you intend).
- **Out of sync** — the configured source produces something different from
  what's running. Open the diff to see what differs and adjust the chart source,
  version, or values until a refresh comes back in sync **before** your first
  upgrade.

You can re-run the comparison any time with **Refresh** on the application's
page (this also requires a job runner).
