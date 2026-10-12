import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { unresolvedFailures } from "./needsAttention";

type WorkflowRun = components["schemas"]["WorkflowRun"];

let n = 0;
function run(over: Partial<WorkflowRun>): WorkflowRun {
  n += 1;
  return {
    id: `r${n}`,
    application_id: "a1",
    action: "deploy",
    status: "succeeded",
    created_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

const ids = (runs: WorkflowRun[]) => unresolvedFailures(runs).map((r) => r.id);

describe("unresolvedFailures", () => {
  it("drops a failure a newer run of the same kind has covered", () => {
    const fixed = run({ id: "fixed", status: "failed" });
    expect(ids([run({ status: "succeeded" }), fixed])).toEqual([]);
    // A newer failure replaces the older one; an in-flight retry hides it.
    expect(ids([run({ id: "again", status: "failed" }), fixed])).toEqual(["again"]);
    expect(ids([run({ status: "running" }), fixed])).toEqual([]);
  });

  it("keeps a failure that is still the latest of its kind", () => {
    const failed = run({ id: "deploy", status: "partial" });
    // A newer preview or drift check does not cover a failed deploy.
    expect(ids([run({ action: "preview" }), run({ action: "drift" }), failed])).toEqual(["deploy"]);
  });

  it("scopes coverage to the application", () => {
    const failed = run({ id: "a1", status: "failed" });
    expect(ids([run({ application_id: "a2" }), failed])).toEqual(["a1"]);
  });

  it("lets a whole-workflow deploy or uninstall cover everything before it", () => {
    const scope = { component_id: "c1", component_name: "db" };
    const older = [
      run({ action: "drift", status: "failed" }),
      run({ action: "preview", status: "failed", trigger: { source: "github", event: "pull_request", repo: "acme/infra", branch: "x", sha: "abc", pr_number: 7 } }),
      run({ action: "deploy", scope, status: "failed" }),
    ];
    expect(ids([run({ action: "uninstall" }), ...older])).toEqual([]);
    // A component deploy covers only that component's earlier runs.
    expect(ids([run({ action: "deploy", scope }), ...older])).toEqual([older[0].id, older[1].id]);
  });

  it("tracks previews per pull request and state operations per component", () => {
    const pr = (num: number, status: WorkflowRun["status"], id?: string) =>
      run({ id, action: "preview", status, trigger: { source: "github", event: "pull_request", repo: "acme/infra", branch: "x", sha: "abc", pr_number: num } });
    expect(ids([pr(8, "succeeded"), pr(7, "failed", "pr7")])).toEqual(["pr7"]);
    expect(ids([pr(7, "succeeded"), pr(7, "failed")])).toEqual([]);

    const stateOp = (component: string, status: WorkflowRun["status"], id?: string) =>
      run({
        id,
        action: "state_op",
        status,
        stages: [{ name: "Stage 1", status: "succeeded", components: [{ component_id: component, name: component, type: "terraform", status: "succeeded", component_run_ids: [] }] }],
      });
    expect(ids([stateOp("c2", "succeeded"), stateOp("c1", "failed", "op")])).toEqual(["op"]);
    expect(ids([stateOp("c1", "succeeded"), stateOp("c1", "failed")])).toEqual([]);
  });
});
