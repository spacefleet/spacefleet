import type { components } from "../../api/schema";

type StateOperation = components["schemas"]["StateOperation"];
type RunScope = components["schemas"]["RunScope"];

// runActionLabel renders a run action for display. Every action but
// `state_op` reads fine as its raw value (capitalised by the caller); a
// state operation is shown as what it is, and a deploy or uninstall limited
// to one component (a run with a scope) says so — an uninstall of one
// component is a destroy.
export function runActionLabel(action: string, scope?: RunScope | null): string {
  if (action === "state_op") return "State operation";
  if (scope) return action === "uninstall" ? "Component destroy" : "Component deploy";
  return action;
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
