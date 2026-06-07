# Importing existing Helm releases

If you already have Helm releases running on a cluster — installed with the
`helm` CLI or by another tool — you can adopt them into Spacefleet as managed
**applications** without redeploying them. Spacefleet reads the release's
current state from the cluster and pre-fills the application's basics from it, so
you can start managing the workload through a deploy workflow.

Adopting a release **does not redeploy it**. The release keeps running exactly
as it is; Spacefleet just starts tracking it as an application. You then build
the application's **deploy workflow** on the canvas — and from then on you
deploy, preview, and uninstall it the same way as any other application. (See
[Deploying with workflows](deploy-workflows.md).)

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
   - **Name**, **target namespace**, and **target cluster** are taken from the
     release.
   - Choose a **runner cluster** — a job-running (Tekton-enabled) cluster that
     will run this application's deploys, previews, and uninstalls. (See
     [Running jobs in a cluster](running-jobs.md).)
3. Select **Import release**.

The application is created as an **imported** application — nothing is deployed,
the live release keeps running untouched — and Spacefleet takes you to the
**workflow canvas** to build its deploy steps.

## Build its workflow

An imported application starts with an empty workflow. Add a **Helm** component
for the release and tell Spacefleet **where the chart comes from** — the part
Helm doesn't record on the cluster:

- an **HTTP Helm repository** (repository URL + chart name),
- an **OCI registry** reference, or
- a **Git repository** the chart lives in (and, optionally, values files pulled
  from a Git repository).

Set the **release name** to match the live release, and fill in the **values**
the release was installed with. (Values passed at install time can contain
secrets; they're stored with the application and shown only to members who can
edit it.) For a private chart or repository, attach a chart credential or GitHub
App installation. See [Deploying with workflows](deploy-workflows.md) for the
full builder walkthrough.

## Confirm the workflow matches before deploying

Before your first **Deploy**, run a **Preview** from the builder. A preview is a
dry run — it changes nothing on the cluster — and reports the **diff** each
component *would* apply. Use it to confirm the chart source, version, and values
you entered reproduce the live release:

- An **empty diff** means the workflow reproduces what's already running, so your
  first deploy will change only what you intend.
- A **non-empty diff** means the workflow would change the live release. Open the
  diff to see what differs, then adjust the chart source, version, or values and
  preview again until it comes back empty.

Previews and deploys both run on the runner cluster you chose, so they need a job
runner that's configured and reachable.
