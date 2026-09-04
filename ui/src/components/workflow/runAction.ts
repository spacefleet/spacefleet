import type { components } from "../../api/schema";

type StateOperation = components["schemas"]["StateOperation"];

// runActionLabel renders a run action for display. Every action but
// `state_op` reads fine as its raw value (capitalised by the caller); a
// state operation is shown as what it is.
export function runActionLabel(action: string): string {
  return action === "state_op" ? "State operation" : action;
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
