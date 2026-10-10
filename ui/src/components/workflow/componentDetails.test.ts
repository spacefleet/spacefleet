import { describe, expect, it } from "vitest";
import { componentDetails } from "./componentDetails";
import type { EditableComponent } from "./ComponentFields";

const lookups = {
  appName: "shop",
  clusterName: (id: string) => ({ c1: "prod" })[id],
  chartCredentialName: () => undefined,
  cloudCredentialName: (id: string) => ({ cc1: "prod-aws" })[id],
};

function component(over: Partial<EditableComponent>): EditableComponent {
  return {
    id: "x",
    name: "infra",
    type: "terraform",
    config: {},
    continue_on_failure: false,
    requires_approval: false,
    approval_policy: null,
    target_cluster_id: null,
    target_namespace: "",
    chart_credential_id: null,
    github_installation_id: null,
    ...over,
  } as EditableComponent;
}

function asMap(c: EditableComponent): Record<string, string> {
  return Object.fromEntries(
    componentDetails(c, lookups).map((r) => [r.label, r.value]),
  );
}

describe("componentDetails", () => {
  it("describes an OpenTofu component, with unset settings as their defaults", () => {
    const rows = asMap(
      component({
        config: {
          repo_url: "https://github.com/acme/infra",
          backend: "s3",
          backend_config: '{"bucket":"state","key":"prod.tfstate"}',
          workspace: "review",
          cloud_credential_id: "cc1",
        },
      }),
    );
    expect(rows).toMatchObject({
      Repository: "https://github.com/acme/infra",
      "Branch or tag": "Default branch",
      "Working path": "Repository root",
      "State backend": "Amazon S3",
      "State location": "s3://state/prod.tfstate",
      Workspace: "review",
      "Cloud credential": "prod-aws",
      Approval: "Not required",
      "On failure": "Later stages don't run",
    });
    expect(rows).not.toHaveProperty("Cluster authentication");
  });

  it("gives managed state no location or workspace, and defaults to the runner's identity", () => {
    const rows = asMap(
      component({ config: { backend: "spacefleet", workspace: "stale" } }),
    );
    expect(rows["State backend"]).toBe("Spacefleet (managed)");
    expect(rows).not.toHaveProperty("State location");
    expect(rows).not.toHaveProperty("Workspace");
    expect(rows["Cloud credential"]).toBe("Runner's identity");
  });

  it("describes a Helm chart and where it deploys", () => {
    const rows = asMap(
      component({
        name: "web",
        type: "helm",
        config: { chart_source: "oci", repo_url: "oci://ghcr.io/acme/charts", chart: "web" },
        target_cluster_id: "c1",
        target_namespace: "apps",
        continue_on_failure: true,
      }),
    );
    expect(rows).toMatchObject({
      "Chart source": "OCI registry",
      "Release name": "shop-web",
      "Target cluster": "prod",
      "Target namespace": "apps",
      "Chart credential": "None (public chart)",
      "On failure": "Later stages still run",
    });
  });

  it("summarizes an approval policy", () => {
    const rows = asMap(
      component({
        requires_approval: true,
        approval_policy: {
          approvers: ["a@acme.dev", "b@acme.dev"],
          required: 2,
          timeout_minutes: 60,
        },
      }),
    );
    expect(rows.Approval).toBe(
      "Required · 2 approvals · from a@acme.dev, b@acme.dev · times out after 60 min",
    );
  });
});
