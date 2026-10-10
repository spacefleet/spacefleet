import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowLayout } from "./WorkflowLayout";
import { WorkflowBuilder } from "./WorkflowBuilder";
import { NodeEditor } from "./NodeEditor";
import { ComponentDetail } from "./ComponentDetail";
import { ComponentVariables } from "./ComponentVariables";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), PUT: vi.fn(), POST: vi.fn() },
}));

// The role is mutable per test so the viewer case can flip it.
const org = { role: "editor" };
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: org.role,
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  PUT: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};

const STAGE_INFRA = "aaaaaaaa-0000-0000-0000-000000000001";
const STAGE_APPS = "aaaaaaaa-0000-0000-0000-000000000002";

// infra (OpenTofu) runs first; release (helm) and apply (manifest) run in
// parallel after it.
const infra = {
  id: "33333333-3333-3333-3333-333333333333",
  name: "infra",
  type: "terraform",
  config: {
    backend: "s3",
    path: "envs/prod",
    backend_config:
      '{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1"}',
  },
  continue_on_failure: false,
  requires_approval: true,
  target_namespace: "",
};
const release = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "release",
  type: "helm",
  config: { chart_source: "http_repo", repo_url: "https://example.com", chart: "web" },
  continue_on_failure: false,
  target_cluster_id: "c1",
  target_namespace: "web",
};
const apply = {
  id: "22222222-2222-2222-2222-222222222222",
  name: "apply",
  type: "manifest",
  config: { repo_url: "https://github.com/org/m.git", path: "m" },
  continue_on_failure: true,
  target_namespace: "",
};

const twoStages = [
  { id: STAGE_INFRA, name: "Infrastructure", components: [infra] },
  { id: STAGE_APPS, name: "Apps", components: [release, apply] },
];

function tree(entry = "/applications/app-1/workflow") {
  return (
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
          <Route index element={<WorkflowBuilder />} />
          <Route path="nodes/:nodeId" element={<ComponentDetail />} />
          <Route path="nodes/:nodeId/edit" element={<NodeEditor />} />
          <Route path="nodes/:nodeId/variables" element={<ComponentVariables />} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

function renderWorkflow(entry?: string) {
  return render(tree(entry));
}

function defaultGets(stages: unknown[], cloudCreds: unknown[] = []) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: { id: "app-1", name: "shop" }, error: undefined });
    if (path === "/api/applications/{id}/workflow")
      return Promise.resolve({ data: { stages }, error: undefined });
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

type SentComponent = {
  id: string;
  name: string;
  type: string;
  requires_approval: boolean;
  target_cluster_id?: string | null;
  config: Record<string, string>;
} & Record<string, unknown>;
type SentStage = { id: string; name: string; components: SentComponent[] };

// lastPut returns the stages of the most recent workflow PUT.
function lastPut(): SentStage[] {
  const calls = mockApi.PUT.mock.calls;
  return (calls[calls.length - 1][1].body as { stages: SentStage[] }).stages;
}

// openEditor opens a saved component's editor the way a person does: its card
// in the builder, then Manage on its page.
async function openEditor(name: string) {
  await userEvent.click(await screen.findByText(name));
  await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
  await screen.findByRole("button", { name: /save component/i });
}

async function waitForPut() {
  await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled(), { timeout: 2000 });
}

function stageColumn(name: string): HTMLElement {
  return screen.getByRole("region", { name: `Stage ${name}` });
}

beforeEach(() => {
  org.role = "editor";
  mockApi.GET.mockReset();
  mockApi.PUT.mockReset();
  mockApi.POST.mockReset();
  // The builder auto-saves dirty edits (debounced), so give every test a benign
  // PUT by default; tests that assert on save behavior override it.
  mockApi.PUT.mockResolvedValue({ data: { stages: [] }, error: undefined });
});

describe("WorkflowBuilder", () => {
  it("renders each stage as a column holding its components in order", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    const infraCol = within(await screen.findByRole("region", { name: "Stage Infrastructure" }));
    expect(infraCol.getByText("infra")).toBeInTheDocument();
    // The module path is the OpenTofu card's summary; the deploy target the
    // helm card's.
    expect(infraCol.getByText("envs/prod")).toBeInTheDocument();
    const apps = within(stageColumn("Apps"));
    expect(apps.getByText("release")).toBeInTheDocument();
    expect(apps.getByText("prod / web")).toBeInTheDocument();
    expect(apps.getByText("apply")).toBeInTheDocument();
    // Gate and continue-on-failure are flagged on their cards.
    expect(infraCol.getByLabelText("Requires approval")).toBeInTheDocument();
    expect(apps.getByLabelText("Continues on failure")).toBeInTheDocument();
  });

  it("Save PUTs the stages in order, each with its components in order", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");

    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitForPut();
    const stages = lastPut();
    expect(stages.map((s) => [s.id, s.name])).toEqual([
      [STAGE_INFRA, "Infrastructure"],
      [STAGE_APPS, "Apps"],
    ]);
    expect(stages[1].components.map((c) => c.id)).toEqual([release.id, apply.id]);
    // No graph fields travel any more — stage order is the only ordering.
    expect(stages[1].components[0]).not.toHaveProperty("depends_on");
    expect(stages[1].components[0]).not.toHaveProperty("position");
    expect(stages[1].components[0]).not.toHaveProperty("group_id");
  });

  it("shows the server validation error inline on a 400", async () => {
    defaultGets(twoStages);
    mockApi.PUT.mockResolvedValue({
      data: undefined,
      error: { message: "every stage needs a name" },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(await screen.findByText("every stage needs a name")).toBeInTheDocument();
  });

  it("asks before moving OpenTofu state to another backend, then saves with the switch confirmed", async () => {
    defaultGets(twoStages);
    mockApi.PUT.mockResolvedValueOnce({
      data: undefined,
      error: {
        code: "backend_change",
        message: "state backend change for infra: the current state still lists resources",
      },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Not saved — this moves existing OpenTofu state");
    expect(alert).toHaveTextContent("state backend change for infra");
    expect(mockApi.PUT.mock.calls[0][1].body).not.toHaveProperty("allow_backend_change");

    await userEvent.click(within(alert).getByRole("button", { name: "Switch the backend anyway" }));
    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalledTimes(2));
    expect(mockApi.PUT.mock.calls[1][1].body.allow_backend_change).toBe(true);
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("seeds a new OpenTofu component on managed state when the server offers it", async () => {
    const original = window.appConfig;
    window.appConfig = { ...original, managedStateEnabled: true };
    try {
      defaultGets([]);
      renderWorkflow();
      await screen.findByText("No components yet.");
      await userEvent.click(
        screen.getByRole("button", { name: "Add a component to Stage 1" }),
      );
      await userEvent.click(screen.getByRole("menuitem", { name: /opentofu/i }));
      await screen.findByDisplayValue("opentofu");
      expect(
        (screen.getByLabelText("State backend") as HTMLSelectElement).value,
      ).toBe("spacefleet");
      await userEvent.click(screen.getByRole("button", { name: /save component/i }));
      await waitForPut();
      expect(lastPut()[0].components[0].config.backend).toBe("spacefleet");
    } finally {
      window.appConfig = original;
    }
  });

  it("offers no run controls — runs are started from the application page", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    expect(screen.queryByRole("button", { name: /deploy/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /preview/i })).not.toBeInTheDocument();
  });

  it("starts an empty workflow with one local stage and doesn't save it untouched", async () => {
    defaultGets([]);
    renderWorkflow();
    expect(await screen.findByRole("region", { name: "Stage Stage 1" })).toBeInTheDocument();
    expect(screen.getByText("No components yet.")).toBeInTheDocument();
    expect(screen.getByText("All changes saved")).toBeInTheDocument();
    // Well past the auto-save debounce: still nothing sent.
    await new Promise((r) => setTimeout(r, 1000));
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("each stage's Add component lists Helm, Manifest, and OpenTofu", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(
      screen.getByRole("button", { name: "Add a component to Apps" }),
    );
    expect(screen.getByRole("menuitem", { name: /helm/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /manifest/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /opentofu/i })).toBeInTheDocument();
  });

  it("adding a component opens its editor and saving puts it in that stage", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");

    await userEvent.click(
      screen.getByRole("button", { name: "Add a component to Apps" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));

    // The editor opens on the new component, placed in the stage it was added
    // from; it's seeded with a valid slug name.
    expect(await screen.findByText(/in stage 2, Apps/)).toBeInTheDocument();
    expect(screen.getByDisplayValue("helm-release")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /save component/i }));

    // Back on the builder, the component sits at the end of Apps and is saved
    // there.
    expect(await within(stageColumn("Apps")).findByText("helm-release")).toBeInTheDocument();
    await waitForPut();
    expect(lastPut()[1].components.map((c) => c.name)).toEqual([
      "release",
      "apply",
      "helm-release",
    ]);
  });

  it("saves OpenTofu as a single gated component with no authored command", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText("No components yet.");

    await userEvent.click(
      screen.getByRole("button", { name: "Add a component to Stage 1" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: /opentofu/i }));
    await screen.findByDisplayValue("opentofu");
    await userEvent.click(screen.getByRole("button", { name: /save component/i }));
    await waitForPut();

    // The local first stage is saved along with its one component.
    const stages = lastPut();
    expect(stages).toHaveLength(1);
    expect(stages[0].name).toBe("Stage 1");
    const tf = stages[0].components;
    expect(tf).toHaveLength(1);
    expect(tf[0].type).toBe("terraform");
    // No authored command — it's synthesized into plan/apply steps at run time.
    expect(tf[0].config.command).toBeUndefined();
    // Gated by default (apply pauses for review).
    expect(tf[0].requires_approval).toBe(true);
    expect(tf[0].config.backend).toBe("s3");
    // New components are seeded onto the newest OpenTofu line (native locking).
    expect(tf[0].config.tofu_version).toBe("1.12");
  });

  it("backing out of a freshly added component without saving discards it", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText("No components yet.");

    await userEvent.click(
      screen.getByRole("button", { name: "Add a component to Stage 1" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));
    await screen.findByRole("button", { name: /save component/i });
    await userEvent.click(screen.getByRole("button", { name: /^cancel$/i }));

    // Back on the builder, nothing was added (and nothing saved).
    expect(await screen.findByText("No components yet.")).toBeInTheDocument();
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("following a breadcrumb out of a freshly added component discards it", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText("No components yet.");

    await userEvent.click(
      screen.getByRole("button", { name: "Add a component to Stage 1" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));
    await screen.findByRole("button", { name: /save component/i });
    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    await userEvent.click(within(trail).getByRole("link", { name: "Workflow" }));

    // Back on the builder, nothing was added (and nothing saved).
    expect(await screen.findByText("No components yet.")).toBeInTheDocument();
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("reloading the create page re-seeds the form instead of 'not found'", async () => {
    defaultGets(twoStages); // the server has no such component (never saved)
    const newId = "44444444-4444-4444-4444-444444444444";
    renderWorkflow(
      `/applications/app-1/workflow/nodes/${newId}/edit?new=terraform&stage=${STAGE_APPS}`,
    );
    expect(await screen.findByDisplayValue("opentofu")).toBeInTheDocument();
    expect(screen.queryByText(/isn’t in this workflow/i)).not.toBeInTheDocument();
    expect(screen.getByText(/in stage 2, Apps/)).toBeInTheDocument();
    // It's a terraform component: the OpenTofu working-path field is present.
    expect(screen.getByText("Working path")).toBeInTheDocument();
  });

  it("clicking a card opens the component's page: its settings, read-only", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await userEvent.click(await screen.findByText("release"));

    expect(await screen.findByRole("heading", { name: "release" })).toBeInTheDocument();
    expect(screen.getByText(/Helm component/i)).toBeInTheDocument();
    expect(screen.getByText("https://example.com")).toBeInTheDocument();
    // The target cluster by name, and the release name it defaults to.
    expect(screen.getByText("prod")).toBeInTheDocument();
    expect(screen.getByText("shop-release")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Variables" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage" })).toBeInTheDocument();
  });

  it("Manage opens the editor, and Cancel goes back to the component's page", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await openEditor("release");
    expect(screen.getByRole("heading", { name: "Manage component" })).toBeInTheDocument();
    expect(screen.getByDisplayValue("release")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    expect(await screen.findByRole("heading", { name: "release" })).toBeInTheDocument();
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("offers a saved component's variables from its page", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await userEvent.click(await screen.findByText("infra"));
    await userEvent.click(await screen.findByRole("button", { name: "Variables" }));

    expect(await screen.findByRole("heading", { name: "Variables" })).toBeInTheDocument();
    expect(screen.getByText(/overriding any app-level variable/)).toBeInTheDocument();
    await waitFor(() =>
      expect(mockApi.GET).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/variables",
        expect.objectContaining({
          params: { path: { id: "app-1", componentId: infra.id } },
        }),
      ),
    );
  });

  // openDelete opens a component's Delete dialog from its page.
  async function openDelete(name: string) {
    await userEvent.click(await screen.findByText(name));
    await userEvent.click(await screen.findByRole("button", { name: `${name} actions` }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    return screen.getByRole("dialog", { name: `Delete ${name}` });
  }

  it("deletes a component left running, saving straight away", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    const dialog = await openDelete("apply");
    // Nothing is preselected; leaving it running names what keeps running.
    const del = within(dialog).getByRole("button", { name: "Delete component" });
    expect(del).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("radio", { name: /leave it running/i }));
    expect(within(dialog).getByText(/keeps running on its cluster/)).toBeInTheDocument();
    await userEvent.click(del);

    // Back on the builder, without it.
    await screen.findByRole("region", { name: "Stage Apps" });
    expect(screen.queryByText("apply")).not.toBeInTheDocument();
    expect(lastPut()[1].components.map((c) => c.name)).toEqual(["release"]);
    const body = mockApi.PUT.mock.calls[mockApi.PUT.mock.calls.length - 1][1].body;
    expect(body.allow_state_deletion).toBeUndefined();
  });

  it("confirms the state deletion for an OpenTofu component on managed state", async () => {
    const managed = { ...infra, config: { backend: "spacefleet", path: "envs/prod" } };
    defaultGets([
      { id: STAGE_INFRA, name: "Infrastructure", components: [managed] },
      { id: STAGE_APPS, name: "Apps", components: [release, apply] },
    ]);
    renderWorkflow();
    const dialog = await openDelete("infra");
    await userEvent.click(within(dialog).getByRole("radio", { name: /leave it running/i }));
    expect(within(dialog).getByText(/state is deleted/)).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: /download the state first/i }),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete component" }));

    await screen.findByRole("region", { name: "Stage Apps" });
    const body = mockApi.PUT.mock.calls[mockApi.PUT.mock.calls.length - 1][1].body;
    expect(body.allow_state_deletion).toBe(true);
    expect(body.stages[0].components).toEqual([]);
  });

  it("keeps the component and shows the server's refusal", async () => {
    defaultGets(twoStages);
    mockApi.PUT.mockResolvedValue({
      data: undefined,
      error: { code: "state_locked", message: "infra's managed state is locked" },
    });
    renderWorkflow();
    const dialog = await openDelete("infra");
    await userEvent.click(within(dialog).getByRole("radio", { name: /leave it running/i }));
    // A cloud backend's state stays in the bucket.
    expect(within(dialog).getByText("s3://my-state/prod/terraform.tfstate")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete component" }));
    expect(await within(dialog).findByText("infra's managed state is locked")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "infra" })).toBeInTheDocument();
  });

  it("starts a destroy of just the component instead of deleting it", async () => {
    defaultGets(twoStages);
    mockApi.POST.mockResolvedValue({ data: { id: "run-7" }, error: undefined });
    render(
      <MemoryRouter initialEntries={["/applications/app-1/workflow"]}>
        <Routes>
          <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
            <Route index element={<WorkflowBuilder />} />
            <Route path="nodes/:nodeId" element={<ComponentDetail />} />
          </Route>
          <Route path="/applications/:appId/runs/:runId" element={<div>run view</div>} />
        </Routes>
      </MemoryRouter>,
    );
    const dialog = await openDelete("infra");
    await userEvent.click(within(dialog).getByRole("radio", { name: /destroy it first/i }));
    await userEvent.click(within(dialog).getByRole("button", { name: /start destroy for approval/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/components/{componentId}/runs",
      { params: { path: { id: "app-1", componentId: infra.id } }, body: { action: "uninstall" } },
    );
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("opens the delete with its question answered after the component's uninstall", async () => {
    defaultGets(twoStages);
    render(
      <MemoryRouter
        initialEntries={[
          {
            pathname: `/applications/app-1/workflow/nodes/${release.id}`,
            state: { deleteAfterUninstall: true },
          },
        ]}
      >
        <Routes>
          <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
            <Route index element={<WorkflowBuilder />} />
            <Route path="nodes/:nodeId" element={<ComponentDetail />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const dialog = await screen.findByRole("dialog", { name: "Delete release" });
    expect(within(dialog).getByText(/its uninstall finished/i)).toBeInTheDocument();
    expect(within(dialog).queryByRole("radio")).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete component" }));
    await screen.findByRole("region", { name: "Stage Apps" });
    expect(lastPut()[1].components.map((c) => c.name)).toEqual(["apply"]);
  });

  it("shows a viewer the component's page, never the editor", async () => {
    org.role = "viewer";
    defaultGets(twoStages);
    renderWorkflow(`/applications/app-1/workflow/nodes/${release.id}/edit`);

    expect(await screen.findByRole("heading", { name: "release" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Variables" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Manage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /actions$/ })).not.toBeInTheDocument();
  });

  it("offers only OpenTofu components of earlier stages as output references", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    // release (Apps) runs after infra (Infrastructure): infra's outputs are
    // offered under its values and its namespace.
    await openEditor("release");
    expect(screen.getAllByRole("button", { name: "infra" })).toHaveLength(2);
  });

  it("offers no output references from the same stage", async () => {
    // A helm component alongside infra in the first stage can't use its outputs.
    defaultGets([
      { id: STAGE_INFRA, name: "Infrastructure", components: [infra, release] },
    ]);
    renderWorkflow();
    await openEditor("release");
    expect(screen.queryByRole("button", { name: "infra" })).not.toBeInTheDocument();
  });

  it("adds a stage at the end", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /add stage/i }));
    expect(screen.getByRole("region", { name: "Stage Stage 3" })).toBeInTheDocument();
    await waitForPut();
    expect(lastPut().map((s) => s.name)).toEqual(["Infrastructure", "Apps", "Stage 3"]);
    expect(lastPut()[2].components).toEqual([]);
  });

  it("renames a stage in place", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    const name = within(stageColumn("Apps")).getByRole("textbox", { name: "Stage name" });
    await userEvent.clear(name);
    await userEvent.type(name, "Services{Enter}");
    await waitForPut();
    expect(lastPut().map((s) => s.name)).toEqual(["Infrastructure", "Services"]);
  });

  it("doesn't save a stage name cleared to nothing", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    const name = within(stageColumn("Apps")).getByRole("textbox", { name: "Stage name" });
    await userEvent.clear(name);
    fireEvent.blur(name);
    expect(name).toHaveValue("Apps");
    await new Promise((r) => setTimeout(r, 1000));
    expect(mockApi.PUT).not.toHaveBeenCalled();
  });

  it("moves a stage later in run order from its menu", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: "Stage Infrastructure actions" }));
    // The first stage can't move left.
    expect(screen.getByRole("menuitem", { name: "Move left" })).toBeDisabled();
    await userEvent.click(screen.getByRole("menuitem", { name: "Move right" }));
    await waitForPut();
    expect(lastPut().map((s) => s.name)).toEqual(["Apps", "Infrastructure"]);
  });

  describe("move", () => {
    const apiStages = [
      { id: "st-api", name: "Services", components: [{ ...apply, id: "c-api", name: "release" }] },
    ];

    function moveGets() {
      defaultGets(twoStages);
      const base = mockApi.GET.getMockImplementation() as (
        path: string,
        opts?: unknown,
      ) => Promise<unknown>;
      mockApi.GET.mockImplementation((path: string, opts?: { params?: { path?: { id?: string } } }) => {
        if (path === "/api/applications")
          return Promise.resolve({
            data: [
              { id: "app-1", name: "shop" },
              { id: "app-2", name: "api" },
            ],
            error: undefined,
          });
        if (path === "/api/applications/{id}/workflow" && opts?.params?.path?.id === "app-2")
          return Promise.resolve({ data: { stages: apiStages }, error: undefined });
        return base(path, opts);
      });
    }

    function moveTree() {
      return (
        <MemoryRouter initialEntries={[`/applications/app-1/workflow/nodes/${release.id}`]}>
          <Routes>
            <Route path="/applications/:appId/workflow" element={<WorkflowLayout />}>
              <Route index element={<WorkflowBuilder />} />
              <Route path="nodes/:nodeId" element={<ComponentDetail />} />
            </Route>
          </Routes>
        </MemoryRouter>
      );
    }

    async function openMove() {
      await userEvent.click(await screen.findByRole("button", { name: "release actions" }));
      await userEvent.click(screen.getByRole("menuitem", { name: /move/i }));
      return screen.getByRole("dialog", { name: "Move release" });
    }

    it("moves a component into another application's new stage, renamed", async () => {
      moveGets();
      mockApi.POST.mockResolvedValue({ data: { ...release }, error: undefined });
      render(moveTree());
      const dialog = await openMove();
      const appSelect = within(dialog).getByRole("combobox", { name: "Application" });
      await waitFor(() => expect(within(appSelect).getByText("api")).toBeInTheDocument());
      await userEvent.selectOptions(appSelect, "app-2");

      // api already has a release: flagged, and the move held back.
      expect(await within(dialog).findByText(/already has a component named release/)).toBeInTheDocument();
      const submit = within(dialog).getByRole("button", { name: "Move component" });
      expect(submit).toBeDisabled();
      const nameInput = within(dialog).getByRole("textbox", { name: "Name" });
      await userEvent.clear(nameInput);
      await userEvent.type(nameInput, "web");
      await userEvent.selectOptions(
        within(dialog).getByRole("combobox", { name: "Stage" }),
        "New stage at the end",
      );
      expect(within(dialog).getByRole("textbox", { name: "New stage name" })).toHaveValue("Stage 2");
      expect(within(dialog).getByText(/run history stays with this application/)).toBeInTheDocument();
      await userEvent.click(submit);

      await waitFor(() =>
        expect(mockApi.POST).toHaveBeenCalledWith(
          "/api/applications/{id}/components/{componentId}/move",
          {
            params: { path: { id: "app-1", componentId: release.id } },
            body: { application_id: "app-2", new_stage_name: "Stage 2", name: "web" },
          },
        ),
      );
      // Nothing was pending, so nothing was saved first.
      expect(mockApi.PUT).not.toHaveBeenCalled();
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("lists this application too, and holds a move to where it already is", async () => {
      moveGets();
      render(moveTree());
      const dialog = await openMove();
      const stageSelect = within(dialog).getByRole("combobox", { name: "Stage" });
      await waitFor(() =>
        expect(within(stageSelect).getByText(/Apps \(where it is now\)/)).toBeInTheDocument(),
      );
      await userEvent.selectOptions(stageSelect, STAGE_APPS);
      expect(within(dialog).getByRole("button", { name: "Move component" })).toBeDisabled();
      await userEvent.selectOptions(stageSelect, STAGE_INFRA);
      expect(within(dialog).getByRole("button", { name: "Move component" })).toBeEnabled();
    });
  });

  it("deletes only an empty stage", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: "Stage Apps actions" }));
    const item = screen.getByRole("menuitem", { name: /delete stage/i });
    expect(item).toBeDisabled();
    expect(item).toHaveTextContent("Delete or move its components first");
    expect(screen.getByText("release")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    // An empty stage goes without asking.
    await userEvent.click(screen.getByRole("button", { name: /add stage/i }));
    await userEvent.click(screen.getByRole("button", { name: "Stage Stage 3 actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: /delete stage/i }));
    expect(screen.queryByRole("region", { name: "Stage Stage 3" })).not.toBeInTheDocument();
    await waitForPut();
    expect(lastPut().map((st) => st.name)).toEqual(["Infrastructure", "Apps"]);
  });

  it("drags a component into another stage", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    const card = (await screen.findByText("infra")).closest("[draggable]") as HTMLElement;
    const dataTransfer = { setData: vi.fn(), effectAllowed: "" };
    fireEvent.dragStart(card, { dataTransfer });
    // Over the Apps column's empty space: lands at the end of it.
    const target = screen.getByTestId(`stage-drop-${STAGE_APPS}`);
    fireEvent.dragOver(target, { dataTransfer });
    fireEvent.drop(target, { dataTransfer });
    fireEvent.dragEnd(card, { dataTransfer });

    await waitForPut();
    expect(lastPut()[0].components).toEqual([]);
    expect(lastPut()[1].components.map((c) => c.name)).toEqual(["release", "apply", "infra"]);
  });

  it("drags a component onto another card to place it beside that card", async () => {
    defaultGets(twoStages);
    renderWorkflow();
    const dragged = (await screen.findByText("apply")).closest("[draggable]") as HTMLElement;
    const over = screen.getByText("release").closest("[draggable]") as HTMLElement;
    const dataTransfer = { setData: vi.fn(), effectAllowed: "" };
    fireEvent.dragStart(dragged, { dataTransfer });
    // The card's top half (jsdom rects are all zero, so clientY < 0 is "above
    // the middle"): drop before release.
    fireEvent.dragOver(over, { dataTransfer, clientY: -1 });
    fireEvent.drop(over, { dataTransfer });
    await waitForPut();
    expect(lastPut()[1].components.map((c) => c.name)).toEqual(["apply", "release"]);
  });

  it("is read-only for a viewer", async () => {
    org.role = "viewer";
    defaultGets(twoStages);
    renderWorkflow();
    await screen.findByText("release");
    expect(screen.queryByRole("button", { name: /add stage/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add a component/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /actions$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Stage name" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
  });
});

// The OpenTofu editor's backend fields, exercised through the builder → editor
// flow (open infra's card, edit, save, read the PUT).
describe("OpenTofu component editor", () => {
  async function openTerraformEditor() {
    renderWorkflow();
    await openEditor("infra");
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

  async function savedInfra(): Promise<SentComponent | undefined> {
    await userEvent.click(screen.getByRole("button", { name: /save component/i }));
    await waitForPut();
    return lastPut()
      .flatMap((s) => s.components)
      .find((c) => c.id === infra.id);
  }

  it("the S3 backend fields read from and write to backend_config", async () => {
    defaultGets(twoStages);
    await openTerraformEditor();

    expect(querySelectWithOption(/amazon s3/i)).toBeDefined();
    const bucket = screen.getByPlaceholderText("my-terraform-state") as HTMLInputElement;
    expect(bucket.value).toBe("my-state");
    const key = screen.getByPlaceholderText("envs/prod/terraform.tfstate") as HTMLInputElement;
    expect(key.value).toBe("prod/terraform.tfstate");
    const region = screen.getByPlaceholderText("us-east-1") as HTMLInputElement;
    expect(region.value).toBe("us-east-1");

    await userEvent.clear(bucket);
    await userEvent.type(bucket, "other-state");
    const tf = await savedInfra();
    expect(tf?.config.backend).toBe("s3");
    expect(JSON.parse(tf?.config.backend_config ?? "{}")).toEqual({
      bucket: "other-state",
      key: "prod/terraform.tfstate",
      region: "us-east-1",
    });
  });

  it("the Cluster authentication select writes auth_cluster_id (not a deploy target)", async () => {
    defaultGets(twoStages);
    await openTerraformEditor();
    await userEvent.click(screen.getByRole("button", { name: /^Credentials/ }));

    const auth = selectWithOption(/^prod$/);
    expect(auth.value).toBe("");
    await userEvent.selectOptions(auth, "c1");
    const tf = await savedInfra();
    expect(tf?.config.auth_cluster_id).toBe("c1");
    // Cluster auth is config, not the component's deploy target (the server
    // rejects a terraform component with one).
    expect(tf?.target_cluster_id ?? null).toBeNull();
  });

  it("the OpenTofu version select adapts the locking UI and writes tofu_version", async () => {
    defaultGets(twoStages);
    await openTerraformEditor();

    // A component from before tofu_version existed shows the server's default
    // line — which locks via DynamoDB.
    const version = selectWithOption(/1\.12 \(latest\)/);
    expect(version.value).toBe("1.9");
    expect(screen.queryByText(/state locking is automatic/i)).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("(no locking)")).toBeInTheDocument();

    await userEvent.selectOptions(version, "1.12");
    expect(screen.getByText(/state locking is automatic/i)).toBeInTheDocument();
    expect(screen.queryByPlaceholderText("(no locking)")).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("(not needed)")).toBeInTheDocument();

    const tf = await savedInfra();
    expect(tf?.config.tofu_version).toBe("1.12");
  });

  it("the DynamoDB lock table writes dynamodb_table into backend_config", async () => {
    defaultGets(twoStages);
    await openTerraformEditor();
    await userEvent.type(screen.getByPlaceholderText("(no locking)"), "tf-locks");
    const tf = await savedInfra();
    expect(JSON.parse(tf?.config.backend_config ?? "{}").dynamodb_table).toBe("tf-locks");
  });

  it("the encrypt toggle reveals the KMS key field and writes encrypt into backend_config", async () => {
    defaultGets(twoStages);
    await openTerraformEditor();

    const encrypt = screen.getByRole("checkbox", {
      name: /encrypt state at rest/i,
    }) as HTMLInputElement;
    expect(encrypt.checked).toBe(false);
    expect(screen.queryByPlaceholderText("(SSE-S3)")).not.toBeInTheDocument();
    await userEvent.click(encrypt);
    expect(screen.getByPlaceholderText("(SSE-S3)")).toBeInTheDocument();

    const tf = await savedInfra();
    expect(JSON.parse(tf?.config.backend_config ?? "{}").encrypt).toBe("true");
  });

  it("the cloud-credential picker lists only the backend's cloud (aws for s3)", async () => {
    defaultGets(twoStages, [
      { id: "cc-aws", name: "prod-aws", provider: "aws", config: {} },
      { id: "cc-gcp", name: "prod-gcp", provider: "gcp", config: {} },
    ]);
    await openTerraformEditor();
    await userEvent.click(screen.getByRole("button", { name: /^Credentials/ }));

    const picker = selectWithOption(/use the runner's identity/i);
    const optionLabels = Array.from(picker.options).map((o) => o.textContent);
    expect(optionLabels).toContain("prod-aws");
    expect(optionLabels).not.toContain("prod-gcp");
    expect(optionLabels).toContain("(none — use the runner's identity)");
  });
});
