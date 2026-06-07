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
3. Add a component with **Helm** or **Manifest**. It appears as a node on the
   canvas.
4. Select a node to edit it in the side panel:
   - **Name** — a label for the step.
   - For a **Helm** component, where the chart comes from (an HTTP Helm
     repository, an OCI registry, or a Git repository), the chart name and
     version, the release name, and the **values** to install it with. For a
     private chart, attach a chart credential or GitHub App installation.
   - For a **Manifest** component, the Git repository, branch or tag, and the
     path to the manifests to apply.
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
