# Deploying with workflows

An **application** in Spacefleet is deployed by a **workflow** — a diagram of the
steps that make it up and the order they run in. Each step is a **component**:
a Helm release to install or upgrade, or a set of Kubernetes manifests to apply.
You arrange the components on a canvas, draw arrows to say which steps wait for
which, and then run the whole thing as one operation.

A workflow is a graph, so a single application can fan out into several pieces —
a database chart, the app's chart, and a one-off manifest — and Spacefleet runs
them in the right order, in parallel where the order allows it.

## Build the workflow

1. Open **Applications** and select the application you want to work on.
2. Select **Workflow** to open the builder canvas.
3. Add a component with **Helm**, **Manifest**, or **OpenTofu**. It appears as
   a node on the canvas.
4. Select a node to edit it in the side panel:
   - **Name** — a label for the step.
   - For a **Helm** component, where the chart comes from (an HTTP Helm
     repository, an OCI registry, or a Git repository), the chart name and
     version, the release name, and the **values** to install it with. For a
     private chart, attach a chart credential or GitHub App installation.
   - For a **Manifest** component, the Git repository, branch or tag, and the
     path to the manifests to apply.
   - For an **OpenTofu** component, the Git repository, branch or tag, and the
     working path holding your OpenTofu files, plus the state backend — and,
     for code that creates Kubernetes resources, optional **cluster
     authentication** — see [OpenTofu components](#opentofu-components).
   - **Target cluster** and **target namespace** — optional per-step overrides.
     Leave them blank to use the application's defaults; set them to send a
     particular step to a different cluster or namespace.
   - **Continue on failure** — when on, a failure of this step does not stop the
     steps that depend on it; the overall run finishes as **partial** instead of
     failed. Leave it off for a step that later steps truly require.
5. Draw an arrow from one node to another to make the second **wait for** the
   first. A step with no incoming arrows starts immediately; steps with no
   unfinished prerequisites run at the same time. The graph must not contain a
   loop — a step can't end up waiting on itself.
6. **Save** the workflow.

The **values** you give a Helm component can contain secrets (passwords, tokens).
They are stored with the application and are only shown back to members who can
edit the application; members with view-only access see the rest of a step's
configuration but not its values.

## OpenTofu components

An OpenTofu component runs your infrastructure code with
[OpenTofu](https://opentofu.org). One node does the full cycle: every run
first produces a **plan** for review; the **apply** then waits for a human
approval by default (turn on **Auto-approve apply** to skip the pause). The
apply always executes exactly the plan that was reviewed — if someone changed
the infrastructure in between, the run fails loudly instead of applying
something different.

### OpenTofu version

Each component picks the OpenTofu release it runs. New components default to
the latest supported release; existing components keep the release they were
created with until you change it. Upgrading is always safe for your state;
OpenTofu does not guarantee that a newer release's state can be read by an
older one, so treat a downgrade as one-way unless you know otherwise.

### State backend

The component's state always lives where the component says — Spacefleet
configures your module's backend at run time, overriding any backend block in
your code. Point the **bucket**, **state key**, and **region** at an Amazon S3
location (an existing state file there is adopted in place). Attach a **cloud
credential** for the run to sign in with, or leave it on the runner's own
instance role.

### State locking

Locking prevents two runs (or a run and a colleague's laptop) from writing the
same state at once and corrupting it.

- **OpenTofu 1.10 and newer** — locking is automatic. State is locked in the
  state bucket itself during every plan and apply; there is nothing to set up
  and no extra infrastructure. If your bucket policy is scoped to the exact
  state key, widen it slightly: the lock is an object next to your state named
  `<state key>.tflock`.
- **OpenTofu 1.9** — name a **DynamoDB lock table**. If the table doesn't
  exist, Spacefleet creates it for you (when a cloud credential is attached;
  a run on the runner's instance role needs the table to already exist). The
  credential needs DynamoDB `DescribeTable`, `GetItem`, `PutItem`, and
  `DeleteItem` on the table — plus `CreateTable` if you want it created for
  you. Leaving the field empty means no locking.

Moving an existing state from DynamoDB locking to the automatic kind: pick
OpenTofu 1.10 or newer and keep the lock table named for as long as anything
else (CI, laptops) still locks that state via DynamoDB — both locks are held
together. Once nothing else uses the table, clear the field.

### Cluster authentication

If your OpenTofu code creates Kubernetes resources — through the `kubernetes`,
`helm`, or `kubectl` provider — set **Cluster authentication** on the
component: pick one of your registered clusters, and the run makes ready-to-use
access to it available to your code. No kubeconfig in your repository, no
cluster credentials in variables — Spacefleet prepares the connection from the
cluster's registration, fresh for the plan step and again for the apply (for a
cluster registered through a cloud provider, that means a short-lived token
minted just before each step runs).

For the providers to pick it up, leave the provider block in your code
unconfigured:

```hcl
provider "kubernetes" {}
```

The run points the standard `KUBE_CONFIG_PATH` environment variable at the
prepared connection, which the `kubernetes`, `helm`, and `kubectl` providers
all read automatically. A provider block that hardcodes a `config_path`,
`host`, or context overrides it — and with nothing set and no cluster
authentication attached, a provider that needs a cluster simply fails, so a
module never silently talks to the wrong one.

Two things to know:

- **It is not a deploy target.** Unlike a Helm or Manifest step, an OpenTofu
  step doesn't deploy *into* a cluster — what your code creates, and where, is
  entirely up to the code. Cluster authentication only supplies credentials;
  pick the cluster your providers are meant to talk to.
- **In-cluster registrations only work from their own cluster.** A cluster
  registered with the **In-cluster** method can be used for cluster
  authentication only when the application's runner cluster is that same
  cluster — otherwise the run fails up front with an error saying so. To use
  the cluster from any runner, register it with another method (a token, a
  kubeconfig, or your cloud provider).

## Run the workflow

The builder has three run actions. Each one runs the **whole** workflow,
respecting the order you drew:

- **Deploy** — install or upgrade every component on its target cluster. This is
  the action that changes your clusters. For a Helm step you can turn on the
  force option so its workloads restart even when the rendered output hasn't
  changed.
- **Preview** — a dry run of the whole workflow. Nothing is applied to any
  cluster; instead each step reports the **diff** it *would* make, so you can see
  what a deploy would change before you run it.
- **Uninstall** — remove every component's release from its cluster.

Runs execute on the application's **runner cluster** — a job-running
(Tekton-enabled) cluster. (See [Running jobs in a cluster](running-jobs.md) for
how to designate one.) Only one run can be in progress for an application at a
time; starting a second while one is still going is refused, so two runs never
fight over the same releases.

## Watch a run

When you start a run, Spacefleet opens the **run view** and shows progress live —
you don't need to refresh. The run moves through **pending → running**, and each
component shows its own status (**pending → running → succeeded / failed**, or
**skipped** when a prerequisite failed and the step couldn't run).

When every step has settled the run reaches a terminal state:

- **Succeeded** — every step succeeded.
- **Failed** — a required step failed, so the run stopped where the failure made
  the rest impossible.
- **Partial** — only steps marked *continue on failure* failed; the run
  finished, but not everything succeeded.

Select a component in the run view to see its detail: the captured log output,
and — for a **Preview** run — the diff that step would apply. (Logs and diffs can
echo a chart's values, so like the values themselves they're shown only to
members who can edit the application.)

## Run history

Every run is kept. Open **Runs** (or **History**) on the application to see past
runs newest-first, with each run's action, status, and when it ran. Open one to
see the same detail as a live run, including a **snapshot of the workflow as it
was when that run started** — so a past run stays accurate even after you've
edited the workflow since.
