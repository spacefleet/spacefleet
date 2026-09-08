---
title: Plan policies
description: Guard OpenTofu applies with policy as code — write Rego rules that are checked against every plan before it is applied, block or warn on violations, scope a policy to one application, and read the verdict on the run.
category: User
tags: [policy, opa, rego, opentofu, terraform, approvals, governance, plan]
---

# Plan policies

A **plan policy** is a rule, written in [Rego](https://www.openpolicyagent.org/docs/latest/policy-language/),
that Spacefleet checks against every OpenTofu plan of the applications it
covers, after the plan and before the apply. Organization admins manage
policies under **Admin → Policies**; anyone in the organization can read
them.

Each policy has an **enforcement** level:

- **Block** — a violation fails the plan step, the apply is skipped, and the
  run fails. Nothing is applied.
- **Warn** — the violation is recorded on the step and shown at the approval
  gate, and the run proceeds.

A policy covers **all applications** in the organization or **one**
application, and can be disabled without deleting it. A **preview** never
fails on a policy: it records the verdict, so previewing is the way to see
what a policy would do before it blocks anything.

## Writing a policy

A policy is a Rego module in `package spacefleet` with a `deny` rule that
collects violation messages. For example, to refuse any plan that destroys a
database:

```rego
package spacefleet

deny contains msg if {
	some r in input.plan.resources
	r.action == "delete"
	startswith(r.type, "aws_db")
	msg := sprintf("%s would be destroyed", [r.address])
}
```

The policy is compiled when you save it, so a syntax error is reported
immediately with the compiler's message. Rego's newer syntax (`contains`,
`if`, `some … in`) is the default.

### What the policy sees

The `input` document is built from the parsed plan — never from the plan's
text or attribute values, which can carry secrets:

| Field | Meaning |
| --- | --- |
| `input.application.id`, `.name` | The application. |
| `input.component.id`, `.name` | The OpenTofu component. |
| `input.run.id`, `.action`, `.started_by` | The run: `deploy`, `uninstall`, or `preview`, and who started it. |
| `input.plan.has_changes` | Whether the plan changes anything. |
| `input.plan.add`, `.change`, `.destroy`, `.replace` | The plan's counts. |
| `input.plan.outputs_changed` | Whether outputs change. |
| `input.plan.resources[]` | One record per planned change. |

Each resource record has `address` (the full address), `module` (the module
path, empty for the root module), `mode` (`managed` or `data`), `type`,
`name`, `action` (`create`, `update`, `replace`, `delete`, `read`, `move`,
`import`, `forget`, or `other`), and `detail` (OpenTofu's one-line summary).

Some more examples:

```rego
# Cap the blast radius of one apply.
deny contains msg if {
	input.plan.destroy > 5
	msg := sprintf("%d resources would be destroyed; split the change up", [input.plan.destroy])
}

# Only the platform team may replace anything in production.
deny contains msg if {
	input.run.action == "deploy"
	input.application.name == "prod-network"
	input.plan.replace > 0
	not endswith(input.run.started_by, "@platform.example.com")
	msg := "replacements in prod-network need the platform team"
}
```

## Reading the verdict

The plan step of a run shows every policy that was evaluated, its
enforcement, and its messages. A blocked step fails with
`blocked by policy: …` naming the policy and its first messages, and the
apply step is skipped. A policy that errors at evaluation (for example a
runtime error) counts as a violation when it blocks — Spacefleet fails
closed rather than applying a plan it could not check.
