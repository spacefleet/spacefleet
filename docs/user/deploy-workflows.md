# Deploying with workflows

An **application** in Spacefleet is deployed by a **workflow**: an ordered list
of **stages**, each holding one or more **components**. A component is one step
of the deploy — a Helm release to install or upgrade, a set of Kubernetes
manifests to apply, or an OpenTofu module to plan and apply.

Stages run one after another, left to right. The components inside a stage run
at the same time, and the next stage starts once every component of the current
one has finished. A typical workflow reads like a sentence: an **Infrastructure**
stage that provisions what the app needs, then an **Apps** stage that deploys
the services onto it, then a **Verify** stage with a smoke test.

## Build the workflow

1. Open **Applications** and select the application you want to work on.
2. Select **Workflow** to open the builder. Each stage is a column; its
   components are listed inside it, top to bottom.
3. Add a stage for each phase of the deploy, and rename it to say what it does
   (click its name). Use the stage's menu to move it left or right, or to
   delete it — deleting a stage removes its components too, so you're asked to
   confirm when it isn't empty.
4. In a stage, select **Add component** and choose **Helm**, **Manifest**, or
   **OpenTofu**. The component editor opens:
   - **Name** — a short, lowercase name for the step (letters, digits, and
     hyphens). Other components refer to it by this name.
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
   - **Target cluster** and **target namespace** — where a Helm release is
     installed (both required), or the cluster a Manifest component applies to.
     OpenTofu components have no deploy target.
   - **Continue on failure** — when on, a failure of this component does not
     stop the later stages; the overall run finishes as **partial** instead of
     failed. Leave it off for a component that later stages truly require.
5. To change the order, drag a component to another place in its stage or into
   another stage, or use the component's menu to move it up, down, or to the
   previous or next stage. Only the stage a component is in affects when it
   runs; its position within the stage is just for reading.
6. Changes save automatically as you make them.

Put a component in a **later stage** than anything it needs. Two components
in the same stage start together, so neither can rely on the other having
finished.

The **values** you give a Helm component can contain secrets (passwords, tokens).
They are stored with the application and are only shown back to members who can
edit the application; members with view-only access see the rest of a step's
configuration but not its values.

The values, release name, and target namespace can also embed `${{ ... }}`
references — your variables (`${{ vars.CUSTOMER_ID }}`) and the run's context,
like the commit being deployed (`${{ run.git_sha_short }}`) — filled in when a
run starts. See [Variables in component configuration](variable-interpolation.md).

## OpenTofu components

An OpenTofu component runs your infrastructure code with
[OpenTofu](https://opentofu.org). One component does the full cycle: every run
first produces a **plan** for review; the **apply** then waits for a human
approval by default (turn on **Auto-approve apply** to skip the pause). The
apply always executes exactly the plan that was reviewed — if someone changed
the infrastructure in between, the run fails loudly instead of applying
something different.

### Reading a plan

Once a plan step settles, Spacefleet reads the plan for you: the totals show
at a glance (`+2 ~1 -1`, with a `±` count when anything is destroyed and
recreated), so a destroy is visible before you approve anything. Open the plan
step, or the apply step that is waiting for approval, and the **Plan** tab
leads with:

- a headline of what the plan does, called out in red when it destroys or
  replaces anything;
- every resource the plan touches, most destructive first, each expandable to
  that resource's own lines of the plan; and
- the full plan text, one click away, for anyone who wants OpenTofu's own words.

The same view backs a **Preview** run, where an OpenTofu step is a single
read-only plan. Members with view-only access see the totals and the resource
list, but not the attribute-level lines or the plan text, since those can echo
configuration values.

### OpenTofu version

Each component picks the OpenTofu release it runs. New components default to
the latest supported release; existing components keep the release they were
created with until you change it. Upgrading is always safe for your state;
OpenTofu does not guarantee that a newer release's state can be read by an
older one, so treat a downgrade as one-way unless you know otherwise.

### Input variables

There are two ways to feed a module its inputs, and they combine.

**Typed inputs.** The component's **Input variables** field takes a JSON
object of variable name to value:

```json
{
  "region": "eu-west-1",
  "replicas": 3,
  "tags": { "team": "core" },
  "cidrs": ["10.0.0.0/8"]
}
```

It is written into the module as an auto-loaded `tfvars` file before every
plan, apply, drift check, and import, so a list, map, or object is authored
as JSON rather than squeezed into a string, and an import resolves the same
values a plan does. The editor flags JSON that would be rejected as you type.
This is configuration, not a place for secrets: the file travels with the
run's script. Put secrets in sensitive Variables instead (next).

**Variables as inputs.** Your [variables](variable-interpolation.md) — at
the group, application, and component level — already reach every component
job as environment variables. Turn on **Expose variables as OpenTofu inputs**
and each one is also passed as a root-module input: a variable named
`region` becomes `var.region`, exactly as if `TF_VAR_region` had been set.
Precedence is the usual one (a component variable beats an application one,
which beats a group one), a sensitive variable stays sensitive on the way
in, and the value must be valid for the input's declared type — a string is
passed as-is, while a list, map, or object is written as HCL (for example
`["a", "b"]`). Variables the module does not declare are ignored.

When both set the same input, the typed input wins over the exposed
variable, and a `-var` plan flag wins over both — OpenTofu's usual order.
Plan flags such as `-var=env=prod` remain available for one-off overrides.

### Workspaces

One module can back several environments without duplicating the code: give
each component a **Workspace** (`staging`, `prod`, …) and it is selected —
created on first use — before every plan, apply, drift check, and state
operation of that component. Each workspace has its own state under the
component's backend: with the Amazon S3 backend, a workspace's state lives
at `env:/<workspace>/<state key>` in the bucket, while the default workspace
(no name) stays at the state key itself. So two components can share one
bucket and key and still keep separate state, as long as their workspaces
differ.

### State backend

The component's state always lives where the component says — Spacefleet
configures your module's backend at run time, overriding any backend block in
your code. Pick one of the three backends and point it at the location (an
existing state file there is adopted in place):

| Backend | Settings |
| --- | --- |
| **Amazon S3** | bucket, state key, region; optionally a DynamoDB lock table and server-side encryption (SSE-S3 or a KMS key) |
| **Google Cloud Storage** | bucket and a prefix (the folder the state lives under); optionally a Cloud KMS key to encrypt the state with |
| **Azure Blob Storage** | storage account, container, and the state file's blob name; optionally the account's resource group, and whether to authenticate with the credential's Azure AD identity instead of the account keys |

Attach a **cloud credential** of the matching cloud for the run to sign in
with — the same credential serves both the state backend and your module's
providers for that cloud (an AWS credential becomes the usual `AWS_*`
variables, a Google Cloud service-account key becomes `GOOGLE_CREDENTIALS`,
an Azure service principal becomes the `ARM_*` variables). Leave it empty to
use the runner's own identity: an instance role, or a workload identity
bound to the jobs namespace.

Each component needs its own state key or prefix — or its own
[workspace](#workspaces) on a shared one.

Backend settings are configuration, not a place for secrets: they are part
of the run and visible to anyone who can read it. A setting that is a
secret — an S3 access key, a customer-supplied Cloud Storage encryption
key, an Azure storage account key or SAS token — is refused when you save.
Each backend reads the same value from an environment variable instead, so
add it as a **sensitive** [variable](variable-interpolation.md) on the
component under that name (for example `GOOGLE_ENCRYPTION_KEY`,
`ARM_ACCESS_KEY`, `AWS_SECRET_ACCESS_KEY`); the error names the one to use.
Key *names* such as an S3 or Cloud KMS key are fine in the settings.

### State locking

Locking prevents two runs (or a run and a colleague's laptop) from writing the
same state at once and corrupting it. Google Cloud Storage and Azure Blob
Storage lock natively; nothing needs setting up. For Amazon S3:

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

Moving an existing S3 state from DynamoDB locking to the automatic kind: pick
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

### Captured outputs

After a successful apply on a **Deploy** run, the module's
[output values](https://opentofu.org/docs/language/values/outputs/) are
captured and kept with that run. Select the apply step in the run view and open
its **Outputs** tab to see them.

Outputs your module marks `sensitive = true` are masked by default: members who
can edit the application can reveal a value with the eye toggle; everyone else
sees only that the output exists. Output values — sensitive ones included —
never appear in the step's logs.

Captured outputs can also be **referenced by downstream Helm components** —
for example, deploying into a namespace the OpenTofu step provisioned — with
`${{ components.<name>.outputs.<key> }}`. See
[Variables in component configuration](variable-interpolation.md).

### Managed resources

After each successful apply, Spacefleet also records **which resources the
module now manages**: every resource and data source in its state, with its
address, type, provider, and the identifier the provider assigned (an instance
id, an ARN, a bucket name). Only those identity fields are recorded — never a
resource's attribute values, so nothing sensitive in your state leaves the
run.

You can see the inventory in two places:

- On the apply step of a run, under its **Resources** tab — the inventory as
  of that apply.
- On the component itself: open it in the workflow builder and scroll to
  **State**, which shows the resources and outputs from the component's most
  recent successful apply, with a link to the run that recorded them. This is
  the place to answer "what does this component own right now?" without
  opening run history.

A long inventory can be filtered by address, type, provider, or id.

A destroy records state too: after a successful **Uninstall**, or a
per-component destroy, the inventory shows what is left — nothing after a
full destroy, the survivors after a targeted one — so the panel never keeps
listing resources that are gone.

### Drift detection

Infrastructure changes outside of OpenTofu — someone resizes an instance in
the console, a bucket is deleted by hand. **Check drift** on the application
runs a read-only *refresh-only* plan on every OpenTofu component and reports
what no longer matches the last apply. Nothing is changed, no planfile is
saved, and no approval is involved; Helm and Manifest components take no part
(the action is refused when the application has no OpenTofu component).

The result appears in three places:

- The run's step opens on a **Drift** tab: a headline verdict and, for each
  drifted resource, whether it was **changed** or **deleted** outside of
  OpenTofu, expandable to the state-versus-real difference.
- The component's **State** panel in the workflow builder carries the latest
  verdict — no drift, drift with the affected addresses, or a check that
  failed — linking to the run that produced it.
- Every ordinary plan also reports drift it noticed, above its planned
  actions, since a deploy will reconcile it.

To reconcile drift, run **Deploy**: the plan shows the drifted resources being
brought back to the configuration (or, if the outside change is what you
want, change the configuration first).

**On a schedule.** Next to **Check drift**, choose how often a check should run
on its own — every hour, 6 hours, day, or week (or never). A scheduled check is
an ordinary drift run, so it appears in the run history like any other. It is
skipped while another run of the application is in progress and tried again
on the next tick, and it never starts while a deploy is waiting for approval.

### State operations

Some state surgery is occasionally unavoidable: a lock left behind by a run
that died, a resource you want OpenTofu to stop managing without destroying
it, a rename that would otherwise become a destroy and a create, or existing
infrastructure you want to adopt. Rather than doing these from a laptop with
production credentials, run them from the component's **State** panel in the
workflow builder, under **Operations** (editor or above):

| Operation | What it runs | Fields |
| --- | --- | --- |
| Stop managing a resource | `tofu state rm` | resource address |
| Rename a resource | `tofu state mv` | current and new address |
| Import existing infrastructure | `tofu import` | resource address and the provider's id |
| Release a stuck state lock | `tofu force-unlock` | the lock id from the "Error acquiring the state lock" message |

You rarely need to hunt for a lock id. When the component's latest run
failed because the state was locked, the **State** panel says **State is
locked** and shows what OpenTofu recorded about the lock — its id, who took
it, and when — with a link to the run that hit it. **Release this lock…**
fills in the force-unlock operation with that id. Check first that the run
holding the lock is really gone: releasing a lock under a run that is still
applying can corrupt the state. The notice clears as soon as a later run
gets through.

These are the only four; there is no free-form command. Each starts a
**State operation** run of a single step that is **always** parked for
approval first: the run shows the exact command that will run, and an editor
or admin approves or rejects it exactly like a gated apply. Nothing touches
state until it is approved. An import carries the component's `-var` /
`-var-file` plan flags so the resource's configuration resolves the same way
it does for a plan.

After the operation succeeds, the component's recorded state (its resources
and outputs) is refreshed, so the **State** panel shows the result. A state
operation counts as a run of the application: it appears in the run history,
and it cannot start while another run is in progress (nor can a deploy start
while one is waiting for approval).

### Destroying one component and targeted runs

A workflow's **Uninstall** removes everything. To take down just one OpenTofu
component — or to plan and apply just that module without running the rest
of the workflow — use **Destroy and targeted runs** on the component's
**State** panel in the workflow builder (editor or above):

- **Destroy this component** plans the destruction of every resource the
  component manages and then **always waits for approval**, whatever the
  component's own approval setting: the run shows the destroy plan, with
  the resources about to go listed first, and an editor or admin approves
  to destroy or rejects to keep everything. The button asks you to confirm
  before the run starts. Once it applies, the component's recorded state
  is refreshed: an empty inventory after a full destroy, the survivors
  after a targeted one.
- **Deploy this component** plans and applies only this module. It keeps the
  component's own approval gate and policy.

Either run can be **targeted**: list resource addresses (one per line, e.g.
`aws_instance.web` or `module.vpc.aws_subnet.private[0]`) and the plan is
limited to those resources — the same as OpenTofu's `-target`. Only
well-formed addresses are accepted; there is no way to pass other flags.
Targeting is for exceptional situations (recovering from an error, working
around a provider bug): a targeted apply leaves the rest of the module
unreconciled, and OpenTofu will flag that in the plan. Follow it with a
normal deploy when you can.

A component-scoped run appears in the run history as a **Component deploy**
or **Component destroy** naming the component and its targets. It counts as
a run of the application: it cannot start while another run is in progress,
and nothing else can start while it is running or waiting for approval.
These runs are available for OpenTofu components only.

## Run the workflow

The application page has three run actions. Each one runs the **whole**
workflow, stage by stage:

- **Deploy** — install or upgrade every component on its target cluster. This is
  the action that changes your clusters. For a Helm step you can turn on the
  force option so its workloads restart even when the rendered output hasn't
  changed.
- **Preview** — a dry run of the whole workflow. Nothing is applied to any
  cluster; instead each step reports the **diff** it *would* make, so you can see
  what a deploy would change before you run it.
- **Uninstall** — remove every component's release from its cluster.

Runs execute on the application's **runner cluster** — a cluster set up to run
workflow jobs (with Tekton). (See [Runner clusters](running-jobs.md) for how to
set one up.) Only one run can be in progress for an application at a
time; starting a second while one is still going is refused, so two runs never
fight over the same releases.

## Triggers

A run can start from GitHub instead of a click, once the operator has
enabled the GitHub App's webhook (see the operator guide). On the
application page, under **Triggers**:

- **On push** — what a push to a tracked branch starts: nothing, a
  **preview**, or a **deploy**. Use preview to see every change land as a
  dry run; use deploy for a continuous-deployment branch.
- **Plan pull requests** — every pull request against a tracked branch
  starts a preview at the pull request's head, and its result is posted on
  the pull request as a **check** named after the application: the plan
  counts for each OpenTofu step and a link to the run. Pull requests from
  forks are never planned.

A component **tracks** a branch when it is attached to a connected GitHub
installation, its repository is the one the event came from, and either its
git ref is that branch or it has no git ref and the branch is the
repository's default. Any component tracking the branch triggers the whole
workflow run, exactly as if you had clicked the action; the run shows what
triggered it, and the run history lists it as started by the GitHub user.
A trigger that arrives while another run is in progress is skipped (a pull
request gets a neutral check saying so — push again once the run finishes).

## Approvals

Any component can **require manual approval**: the run parks at that step
(and an OpenTofu component does so by default, between its plan and its
apply) until someone decides. By default any editor or admin of the
organization can approve, one approval is enough, the person who started the
run may approve it themselves, and a parked run waits indefinitely.

Each gated component can tighten that with an **approval policy**:

- **Approvers** — the people, by email, who may approve. Anyone with edit
  access can still *reject*; refusing a change never needs the list.
- **Approvals required** — how many distinct approvals open the gate
  (two-of-three, say). Until the count is reached the step stays parked and
  shows who has approved so far.
- **Require a different approver** — the person who started the run cannot
  approve it. A scheduled run has no starter, so anyone may approve it.
- **Approval timeout** — if nobody decides within this many minutes of the
  step parking, the run fails. Steps after it are skipped, exactly as if the
  step had been rejected.

The policy that applies to a run is the one in force when the run started;
editing a component while a run is parked does not change that run's gate.
Every run also records who started it, shown on the run page.

## Watch a run

When you start a run, Spacefleet opens the **run view** and shows progress live —
you don't need to refresh. The run moves through **pending → running**. Down the
left side are the run's stages and their components, each with its own status
(**pending → running → succeeded / failed**, or **skipped** when an earlier stage
failed and the component couldn't run); a stage's status sums up its components.
An OpenTofu component lists its two steps, **plan** and **apply**, underneath it.

When every step has settled the run reaches a terminal state:

- **Succeeded** — every step succeeded.
- **Failed** — a required step failed, so the run stopped where the failure made
  the rest impossible.
- **Partial** — only steps marked *continue on failure* failed; the run
  finished, but not everything succeeded.

Select a component (or one of its steps) to see its detail: its log output —
followed live while the step is still running, then kept once it settles — and,
for a **Preview** run, the diff that step would apply. Until you pick one, the
view follows the step that most needs attention: one waiting for approval, then
a failure, then whatever is running. The address bar keeps the step you
selected, so you can share a link straight to a failed step's logs. (Logs and
diffs can echo a chart's values, so like the values themselves they're shown
only to members who can edit the application.)

## Run history

Every run is kept. Open **Runs** (or **History**) on the application to see past
runs newest-first, with each run's action, status, when it ran, and a bar with
one segment per stage colored by how that stage went — so a failure is easy to
spot without opening anything. Open one to see the same detail as a live run,
including a **snapshot of the workflow as it was when that run started** — so a
past run stays accurate even after you've edited the workflow since.

Runs from before workflows had stages are shown in stages too: each component
is placed after everything it used to wait on, and the stages are numbered.
