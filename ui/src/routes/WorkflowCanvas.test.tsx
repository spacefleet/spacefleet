import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowLayout } from "./WorkflowLayout";
import { WorkflowCanvas } from "./WorkflowCanvas";
import { NodeEditor } from "./NodeEditor";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), PUT: vi.fn(), POST: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "editor",
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  PUT: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};

// A two-node workflow: a helm release that a manifest apply depends on. The edge
// release -> apply is expressed by apply.depends_on = [release.id].
const release = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "release",
  type: "helm",
  config: { chart_source: "http_repo", repo_url: "https://example.com", chart: "web" },
  depends_on: [],
  continue_on_failure: false,
  target_namespace: "",
  position: { x: 100, y: 100 },
};
const apply = {
  id: "22222222-2222-2222-2222-222222222222",
  name: "apply",
  type: "manifest",
  config: { repo_url: "https://github.com/org/m.git", path: "m" },
  depends_on: [release.id],
  continue_on_failure: false,
  target_namespace: "",
  position: { x: 100, y: 300 },
};

function renderWorkflow() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/workflow"]}>
      <Routes>
        <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
          <Route index element={<WorkflowCanvas />} />
          <Route path="nodes/:nodeId" element={<NodeEditor />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function defaultGets(
  comps: unknown[],
  groups: unknown[] = [],
  cloudCreds: unknown[] = [],
) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}/workflow")
      return Promise.resolve({ data: { components: comps, groups }, error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
    if (path === "/api/chart-credentials")
      return Promise.resolve({ data: [], error: undefined });
    if (path === "/api/cloud-credentials")
      return Promise.resolve({ data: cloudCreds, error: undefined });
    if (path === "/api/github/installations")
      return Promise.resolve({ data: [], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.PUT.mockReset();
  mockApi.POST.mockReset();
  // The canvas auto-saves dirty edits (debounced), so give every test a benign
  // PUT by default; tests that assert on save behavior override it.
  mockApi.PUT.mockResolvedValue({
    data: { components: [], groups: [] },
    error: undefined,
  });
});

describe("WorkflowCanvas", () => {
  it("renders nodes loaded from the workflow", async () => {
    defaultGets([release, apply]);
    renderWorkflow();
    expect(await screen.findByText("release")).toBeInTheDocument();
    expect(screen.getByText("apply")).toBeInTheDocument();
  });

  it("Save PUTs components (depends_on from edges) and a groups array", async () => {
    defaultGets([release, apply]);
    mockApi.PUT.mockResolvedValue({
      data: { components: [], groups: [] },
      error: undefined,
    });
    renderWorkflow();
    await screen.findByText("release");

    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled());
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; depends_on: string[]; group_id: string | null }[];
      groups: unknown[];
    };
    const sent = Object.fromEntries(body.components.map((c) => [c.id, c]));
    expect(sent[apply.id].depends_on).toEqual([release.id]);
    expect(sent[release.id].depends_on).toEqual([]);
    expect(sent[apply.id].group_id).toBeNull();
    expect(Array.isArray(body.groups)).toBe(true);
    const sentApply = body.components.find((c) => c.id === apply.id) as unknown as {
      position: { x: number; y: number };
    };
    expect(sentApply.position).toEqual({ x: 100, y: 300 });
  });

  it("shows the server validation error inline on a 400", async () => {
    defaultGets([release]);
    mockApi.PUT.mockResolvedValue({
      data: undefined,
      error: { message: "workflow has a cycle" },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(await screen.findByText("workflow has a cycle")).toBeInTheDocument();
  });

  it("offers no run controls — runs are started from the application page", async () => {
    defaultGets([release]);
    renderWorkflow();
    await screen.findByText("release");
    expect(
      screen.queryByRole("button", { name: /deploy/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /preview/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /run history/i }),
    ).not.toBeInTheDocument();
  });

  it("the + Add dropdown lists Helm, Manifest, OpenTofu, and Group and can add a node", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText(/no components yet/i);

    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));
    // The menu lists every addable type, driven by the items array.
    expect(screen.getByRole("menuitem", { name: /helm/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /manifest/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /opentofu/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /group/i })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));
    expect(await screen.findByText("helm release")).toBeInTheDocument();
  });

  it("adding OpenTofu opens a single create form and saves one node", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText(/no components yet/i);

    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /opentofu/i }));

    // Adding opens the create form for one OpenTofu node (no plan/apply split).
    // It's provisional until saved; Save node commits it and returns to the
    // canvas where the single node is shown.
    await screen.findByText("opentofu");
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    expect(await screen.findByText("opentofu")).toBeInTheDocument();
    expect(screen.queryByText("terraform plan")).not.toBeInTheDocument();
    expect(screen.queryByText("terraform apply")).not.toBeInTheDocument();
  });

  it("backing out of a freshly added node without saving discards it", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText(/no components yet/i);

    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));

    // The editor opens on the new (provisional) node; Cancel drops it.
    await screen.findByRole("button", { name: /save node/i });
    await userEvent.click(screen.getByRole("button", { name: /^cancel$/i }));

    // Back on the canvas, nothing was added.
    expect(await screen.findByText(/no components yet/i)).toBeInTheDocument();
  });

  it("saves OpenTofu as a single gated node with no authored command", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText(/no components yet/i);

    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /opentofu/i }));
    await screen.findByText("opentofu");

    // The new node is provisional until committed; Save node commits it, after
    // which the debounced auto-save persists it as a single terraform component.
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });

    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: {
        id: string;
        name: string;
        type: string;
        depends_on: string[];
        requires_approval: boolean;
        config: Record<string, string>;
      }[];
    };
    const tf = body.components.filter((c) => c.type === "terraform");
    expect(tf).toHaveLength(1);
    // No authored command — it's synthesized into plan/apply units at run time.
    expect(tf[0].config.command).toBeUndefined();
    // Gated by default (apply pauses for review).
    expect(tf[0].requires_approval).toBe(true);
    expect(tf[0].config.backend).toBe("s3");
    // New nodes are seeded onto the newest OpenTofu line (native locking).
    expect(tf[0].config.tofu_version).toBe("1.12");
  });

  it("reloading the create page re-seeds the form instead of 'node not found'", async () => {
    defaultGets([]); // server has no such node (it was never saved)

    // Land directly on the editor's create route with ?new=terraform, as a page
    // reload would — the node lives only in the URL, not on the server.
    const newId = "44444444-4444-4444-4444-444444444444";
    render(
      <MemoryRouter
        initialEntries={[
          `/applications/app-1/workflow/nodes/${newId}?new=terraform`,
        ]}
      >
        <Routes>
          <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
            <Route index element={<WorkflowCanvas />} />
            <Route path="nodes/:nodeId" element={<NodeEditor />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    // The create form is shown (seeded OpenTofu node), not the not-found message.
    expect(await screen.findByText("opentofu")).toBeInTheDocument();
    expect(
      screen.queryByText(/isn’t in this workflow/i),
    ).not.toBeInTheDocument();
    // It's a terraform node: the OpenTofu working-path field is present.
    expect(screen.getByText(/holding the OpenTofu files/i)).toBeInTheDocument();
  });

  // A terraform plan node already loaded in the workflow, so we can open it in
  // the node editor and exercise the S3 backend fields directly.
  const tfPlan = {
    id: "33333333-3333-3333-3333-333333333333",
    name: "terraform plan",
    type: "terraform",
    config: {
      command: "plan",
      backend: "s3",
      backend_config:
        '{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1"}',
    },
    depends_on: [],
    continue_on_failure: false,
    target_namespace: "",
    position: { x: 100, y: 100 },
  };

  async function openTerraformEditor() {
    renderWorkflow();
    await screen.findByText("terraform plan");
    const node = document.querySelector(".react-flow__node") as HTMLElement;
    fireEvent.click(node);
    await userEvent.click(await screen.findByRole("button", { name: /^edit$/i }));
    await screen.findByRole("button", { name: /back to workflow/i });
  }

  // The Field helper renders an adjacent <label> (not linked via htmlFor), so the
  // selects are located through their option text rather than getByLabelText.
  function selectWithOption(text: RegExp): HTMLSelectElement {
    const combos = screen.getAllByRole("combobox") as HTMLSelectElement[];
    const found = combos.find((c) =>
      Array.from(c.options).some((o) => text.test(o.textContent ?? "")),
    );
    if (!found) throw new Error(`no select with option matching ${text}`);
    return found;
  }
  function querySelectWithOption(text: RegExp): HTMLSelectElement | undefined {
    const combos = screen.queryAllByRole("combobox") as HTMLSelectElement[];
    return combos.find((c) =>
      Array.from(c.options).some((o) => text.test(o.textContent ?? "")),
    );
  }

  it("the S3 backend fields read from and write to backend_config", async () => {
    defaultGets([tfPlan], [], []);
    await openTerraformEditor();

    // The state backend is fixed to S3 and the dedicated fields are populated
    // from the stored backend_config JSON.
    expect(querySelectWithOption(/amazon s3/i)).toBeDefined();
    const bucket = screen.getByPlaceholderText(
      "my-terraform-state",
    ) as HTMLInputElement;
    expect(bucket.value).toBe("my-state");
    const key = screen.getByPlaceholderText(
      "envs/prod/terraform.tfstate",
    ) as HTMLInputElement;
    expect(key.value).toBe("prod/terraform.tfstate");
    const region = screen.getByPlaceholderText("us-east-1") as HTMLInputElement;
    expect(region.value).toBe("us-east-1");

    // Editing a field serializes back into the backend_config JSON the save
    // payload carries.
    await userEvent.clear(bucket);
    await userEvent.type(bucket, "other-state");
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; config: Record<string, string> }[];
    };
    const tf = body.components.find((c) => c.id === tfPlan.id);
    expect(tf?.config.backend).toBe("s3");
    expect(JSON.parse(tf?.config.backend_config ?? "{}")).toEqual({
      bucket: "other-state",
      key: "prod/terraform.tfstate",
      region: "us-east-1",
    });
  });

  it("the Cluster authentication select writes auth_cluster_id (not a deploy target)", async () => {
    defaultGets([tfPlan], [], []);
    await openTerraformEditor();

    // Terraform hides the target-cluster field, so the only select offering a
    // registered cluster is the Cluster authentication picker.
    const auth = selectWithOption(/^prod$/);
    expect(auth.value).toBe("");
    await userEvent.selectOptions(auth, "c1");
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: {
        id: string;
        config: Record<string, string>;
        target_cluster_id?: string | null;
      }[];
    };
    const tf = body.components.find((c) => c.id === tfPlan.id);
    expect(tf?.config.auth_cluster_id).toBe("c1");
    // Cluster auth is config, not the node-level deploy target (the server
    // rejects a terraform node with one).
    expect(tf?.target_cluster_id ?? null).toBeNull();
  });

  it("the OpenTofu version select adapts the locking UI and writes tofu_version", async () => {
    defaultGets([tfPlan], [], []);
    await openTerraformEditor();

    // A node from before tofu_version existed shows the server's default line
    // — which locks via DynamoDB, so the lock-table field is the recommended
    // path and there is no "automatic" note.
    const version = selectWithOption(/1\.12 \(latest\)/);
    expect(version.value).toBe("1.9");
    expect(screen.queryByText(/state locking is automatic/i)).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("(no locking)")).toBeInTheDocument();

    // Switching to a native-locking line flips the section: automatic note,
    // and the DynamoDB table demoted to a migration-only field.
    await userEvent.selectOptions(version, "1.12");
    expect(screen.getByText(/state locking is automatic/i)).toBeInTheDocument();
    expect(screen.queryByPlaceholderText("(no locking)")).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("(not needed)")).toBeInTheDocument();

    // The choice persists into the saved config.
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; config: Record<string, string> }[];
    };
    const tf = body.components.find((c) => c.id === tfPlan.id);
    expect(tf?.config.tofu_version).toBe("1.12");
  });

  it("the DynamoDB lock table writes dynamodb_table into backend_config", async () => {
    defaultGets([tfPlan], [], []);
    await openTerraformEditor();

    const table = screen.getByPlaceholderText("(no locking)") as HTMLInputElement;
    await userEvent.type(table, "tf-locks");
    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; config: Record<string, string> }[];
    };
    const tf = body.components.find((c) => c.id === tfPlan.id);
    expect(JSON.parse(tf?.config.backend_config ?? "{}").dynamodb_table).toBe(
      "tf-locks",
    );
  });

  it("the encrypt toggle reveals the KMS key field and writes encrypt into backend_config", async () => {
    defaultGets([tfPlan], [], []);
    await openTerraformEditor();

    // Off by default (not in the stored config): no KMS field.
    const encrypt = screen.getByRole("checkbox", {
      name: /encrypt state at rest/i,
    }) as HTMLInputElement;
    expect(encrypt.checked).toBe(false);
    expect(screen.queryByPlaceholderText("(SSE-S3)")).not.toBeInTheDocument();

    await userEvent.click(encrypt);
    expect(screen.getByPlaceholderText("(SSE-S3)")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /save node/i }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), {
      timeout: 2000,
    });
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; config: Record<string, string> }[];
    };
    const tf = body.components.find((c) => c.id === tfPlan.id);
    expect(JSON.parse(tf?.config.backend_config ?? "{}").encrypt).toBe("true");
  });

  it("the cloud-credential picker lists only aws-provider credentials", async () => {
    defaultGets([tfPlan], [], [
      { id: "cc-aws", name: "prod-aws", provider: "aws", config: {} },
      { id: "cc-gcp", name: "prod-gcp", provider: "gcp", config: {} },
    ]);
    await openTerraformEditor();

    const picker = selectWithOption(/use instance role/i);
    const optionLabels = Array.from(picker.options).map((o) => o.textContent);
    expect(optionLabels).toContain("prod-aws");
    expect(optionLabels).not.toContain("prod-gcp");
    // The instance-role default is always offered.
    expect(optionLabels).toContain("(none — use instance role)");
  });

  it("selecting a node and clicking Edit navigates to the node editor route", async () => {
    defaultGets([release]);
    renderWorkflow();
    await screen.findByText("release");
    // fireEvent.click avoids the user-event pointer sequence that triggers React
    // Flow's d3-drag mousedown handler (which throws in jsdom); a plain click is
    // all the canvas's onNodeClick needs to select the node.
    const node = document.querySelector(".react-flow__node") as HTMLElement;
    fireEvent.click(node);
    await userEvent.click(await screen.findByRole("button", { name: /^edit$/i }));
    // The full-page editor renders ComponentFields (the Name field + Delete node).
    expect(
      await screen.findByRole("button", { name: /back to workflow/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /delete node/i }),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue("release")).toBeInTheDocument();
  });
});
