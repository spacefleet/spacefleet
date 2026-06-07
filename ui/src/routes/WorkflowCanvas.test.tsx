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
        <Route
          path="/applications/:appId/runs/:runId"
          element={<div>run view</div>}
        />
      </Routes>
    </MemoryRouter>,
  );
}

function defaultGets(comps: unknown[], groups: unknown[] = []) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}/workflow")
      return Promise.resolve({ data: { components: comps, groups }, error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
    if (path === "/api/chart-credentials")
      return Promise.resolve({ data: [], error: undefined });
    if (path === "/api/github/installations")
      return Promise.resolve({ data: [], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.PUT.mockReset();
  mockApi.POST.mockReset();
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

  it("starts a deploy run and navigates to the run view", async () => {
    defaultGets([release]);
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /deploy/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: false } }),
    );
  });

  it("sends force=true on deploy when the Force workload roll toggle is on", async () => {
    defaultGets([release]);
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(
      screen.getByRole("checkbox", { name: /force workload roll/i }),
    );
    await userEvent.click(screen.getByRole("button", { name: /deploy/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: true } }),
    );
  });

  it("shows an in-progress message on a 409", async () => {
    defaultGets([release]);
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderWorkflow();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /deploy/i }));
    expect(
      await screen.findByText(/a run is already in progress/i),
    ).toBeInTheDocument();
  });

  it("the + Add dropdown lists Helm, Manifest, and Group and can add a node", async () => {
    defaultGets([]);
    renderWorkflow();
    await screen.findByText(/no components yet/i);

    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));
    // The menu lists every addable type, driven by the items array.
    expect(screen.getByRole("menuitem", { name: /helm/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /manifest/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /group/i })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("menuitem", { name: /helm/i }));
    expect(await screen.findByText("helm release")).toBeInTheDocument();
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
