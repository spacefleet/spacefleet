# Variables in component configuration

The configuration of a workflow's Helm components can embed **references** that
Spacefleet fills in when a run starts, so one workflow serves many
environments and every deploy can pick up values that change from run to run —
a customer identifier, an image tag matching the exact commit being deployed,
and so on.

A reference looks like this:

```yaml
serviceAccount:
  name: "customer-${{ vars.CUSTOMER_ID }}"
containers:
  app:
    image:
      tag: "${{ run.git_sha_short }}"
```

References work in a Helm component's:

- **values** — the inline values you type in the component editor,
- **release name**,
- **target namespace**.

They are substituted for every run action — a **Preview**'s diff shows exactly
what a **Deploy** would apply. Values files pulled from a Git repository (the
*values from Git* sources) are used as-is and are **not** substituted.

## `vars.*` — your variables

`${{ vars.NAME }}` inserts the value of the variable `NAME`. Define variables
in the **Variables** section of the application (or on a single component —
a component's variable overrides an application one of the same name). A
**sensitive** variable can be referenced like any other; its value is handled
with the same care as the values themselves and is never shown in run logs or
to members with view-only access.

If a run starts and a referenced variable doesn't exist, that step fails
immediately with a message naming the missing variable — nothing is deployed
for that step.

## `run.*` — the run's context

| Reference | Value |
| --- | --- |
| `${{ run.id }}` | The unique id of this run. |
| `${{ run.action }}` | `deploy`, `uninstall`, or `preview`. |
| `${{ run.git_ref }}` | The branch or tag the component is configured to deploy from. Requires the component to name an explicit branch/tag. |
| `${{ run.git_sha }}` | The full commit hash the chart clone resolved to. |
| `${{ run.git_sha_short }}` | The same commit, shortened to 7 characters — the usual shape of an image tag. |

`run.git_sha` and `run.git_sha_short` come from the very checkout being
deployed, so they always match the chart contents. Two constraints follow:

- they only work on a component whose **chart source is a Git repository**, and
- they can only appear in the **values** (the namespace and release name are
  needed before the repository is cloned).

## `components.*` — outputs of upstream OpenTofu components

`${{ components.NAME.outputs.KEY }}` inserts an
[output value](https://opentofu.org/docs/language/values/outputs/) of the
OpenTofu component called `NAME` in the same workflow — the glue that lets
infrastructure flow into the deployments built on it. A typical two-step
workflow: an OpenTofu component `infra` provisions a namespace and exposes it
as an output, and a Helm component deploys into it:

```yaml
# the infra module
output "namespace" {
  value = kubernetes_namespace.customer.metadata[0].name
}
```

…then the Helm component's **target namespace** is
`${{ components.infra.outputs.namespace }}`, and its values can reference
further outputs the same way.

The referenced component must be an **OpenTofu component upstream of the
referencing one** — connected before it in the workflow, directly or through
other steps — which is exactly what guarantees its apply has finished before
the value is needed. The editor offers an *insert an output reference* shortcut
under the values and namespace fields listing the components you can use, and
saving a workflow that references a component that isn't upstream (or doesn't
exist, or shares its name with another component) is rejected with a message
naming the problem.

Which value is used:

- On a **Deploy**, the outputs captured by the referenced component's apply
  **in this same run** — so a freshly provisioned namespace is used by the very
  deploy that created it. If that step captured nothing this run (for example,
  it was allowed to continue on failure), the most recent successfully captured
  outputs are used instead.
- On a **Preview**, the most recent successfully captured outputs — a preview
  applies nothing, so it diffs against what the last deploy recorded.
- If the component has **never** deployed successfully with outputs, the
  referencing step fails with a message telling you to deploy it first. The
  same happens for an output name the module doesn't have.

String outputs are inserted as-is; numbers, lists, and objects are inserted in
their JSON form. An output marked `sensitive = true` can be referenced like any
other and is handled with the same care as sensitive variables.

Renaming a component that others reference breaks those references — the
editor warns you when a rename would do that, and the fix is updating the
references to the new name.

## Writing a literal `${{`

If your values need the text `${{` itself (for example, documentation inside a
config file), escape it as `$${{`:

```yaml
note: "$${{ this is not a reference }}"
```

Helm's own `{{ ... }}` templating and shell-style `$NAME` strings are
unrelated syntax and pass through untouched.

## When something is wrong

- A malformed reference (for example, a missing `}}`), an unknown name like
  `${{ env.HOME }}`, a `run.*` key used where it can't work, or a
  `components.*` reference to a component that doesn't exist, isn't an OpenTofu
  component, or isn't upstream of the referencing one is rejected when you
  **save** the workflow, with a message pointing at the problem.
- A reference to a variable that doesn't exist, or to an output that was never
  captured, fails the **step at run time** (both can change independently of
  the workflow, so this can only be checked when the run starts).
