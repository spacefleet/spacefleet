import type { components } from "../../api/schema";

type StateOperation = components["schemas"]["StateOperation"];
type RunScope = components["schemas"]["RunScope"];
type RunTrigger = components["schemas"]["RunTrigger"];

// runActionLabel renders a run action for display. Every action but
// `state_op` reads fine as its raw value (capitalised by the caller); a
// state operation is shown as what it is, and a deploy or uninstall limited
// to one component (a run with a scope) says so — an uninstall of one
// OpenTofu component is a destroy.
export function runActionLabel(action: string, scope?: RunScope | null): string {
  if (action === "state_op") return "State operation";
  if (scope) {
    if (action !== "uninstall") return "Component deploy";
    return isTofuScope(scope) ? "Component destroy" : "Component uninstall";
  }
  return action;
}

// isTofuScope reports whether a scoped run covered an OpenTofu component
// (runs from before the type was recorded were all OpenTofu).
export function isTofuScope(scope: RunScope): boolean {
  return (scope.component_type ?? "terraform") === "terraform";
}

// runScopeDescription is the one-line reading of a component-scoped run:
// which component, and the resource addresses it was targeted to, if any.
export function runScopeDescription(scope: RunScope): string {
  const targets = scope.targets ?? [];
  if (targets.length === 0) return `Only ${scope.component_name}`;
  return `Only ${scope.component_name}, targeting ${targets.join(", ")}`;
}

// stateOpDescription is the one-line human reading of a state operation,
// shown beside its exact command.
export function stateOpDescription(op: StateOperation): string {
  switch (op.operation) {
    case "force_unlock":
      return "Release the state lock";
    case "rm":
      return `Stop managing ${op.address ?? ""} (the resource is not destroyed)`;
    case "mv":
      return `Rename ${op.address ?? ""} to ${op.new_address ?? ""} in state`;
    case "import":
      return `Import ${op.import_id ?? ""} as ${op.address ?? ""}`;
    default:
      return "State operation";
  }
}

// runTriggerDescription is the one-line reading of what started a triggered
// run: the push or pull request, its branch and commit, and who caused it.
export function runTriggerDescription(t: RunTrigger): string {
  const sha = t.sha ? t.sha.slice(0, 7) : "";
  const by = t.sender ? ` by ${t.sender}` : "";
  if (t.event === "pull_request") {
    return `Triggered by pull request #${t.pr_number ?? "?"} on ${t.repo} (${t.branch}${sha ? ` @ ${sha}` : ""})${by}`;
  }
  return `Triggered by a push to ${t.branch} on ${t.repo}${sha ? ` (${sha})` : ""}${by}`;
}

// runStartError is the message for a run that could not start: a 409 means
// the application already has a run in flight; anything else carries the
// server's message.
export function runStartError(
  error: { message?: string } | undefined,
  status: number | undefined,
): string {
  if (status === 409) return "A run is already in progress for this application.";
  return error?.message ?? "Could not start the run";
}
