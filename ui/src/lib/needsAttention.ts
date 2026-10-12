import type { components } from "../api/schema";

type WorkflowRun = components["schemas"]["WorkflowRun"];

const FAILED = new Set(["failed", "partial"]);

// unresolvedFailures picks, from runs listed newest-first, the failed (or
// partial) runs that are still the latest word on what they did — a failure
// a newer run has since covered is history, not something to act on. A
// newer run covers an older one of the same application when it:
//   - deploys or uninstalls the whole workflow (it covers everything), or
//   - re-runs the same kind of work: a deploy/uninstall of the same component,
//     a drift check, a preview of the same pull request (or another manual
//     preview), a state operation on the same component.
// Any outcome covers: a newer failure is listed in place of the older one,
// and an in-flight retry is already being handled.
export function unresolvedFailures(runs: WorkflowRun[]): WorkflowRun[] {
  const wholeApplied = new Set<string>();
  const seen = new Set<string>();
  const out: WorkflowRun[] = [];
  for (const r of runs) {
    const key = `${r.application_id}/${coverageTrack(r)}`;
    if (FAILED.has(r.status) && !wholeApplied.has(r.application_id) && !seen.has(key)) {
      out.push(r);
    }
    if ((r.action === "deploy" || r.action === "uninstall") && !r.scope) {
      wholeApplied.add(r.application_id);
    }
    seen.add(key);
  }
  return out;
}

// coverageTrack names the kind of work a run did, so a newer run on the same
// track supersedes an older one.
function coverageTrack(r: WorkflowRun): string {
  switch (r.action) {
    case "deploy":
    case "uninstall":
      return r.scope ? `apply:${r.scope.component_id}` : "apply";
    case "preview":
      return `preview:${r.trigger?.pr_number ?? "manual"}`;
    case "state_op":
      // A state operation runs one component; the list carries it on the
      // run's stage summary. Without one the run stands alone.
      return `state_op:${r.stages?.[0]?.components[0]?.component_id ?? r.id}`;
    default:
      return r.action;
  }
}
