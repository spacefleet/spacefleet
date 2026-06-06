import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowBuilder } from "./WorkflowBuilder";
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

function renderBuilder() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/workflow"]}>
      <Routes>
        <Route
          path="/applications/:appId/workflow"
          element={<WorkflowBuilder />}
        />
        <Route
          path="/applications/:appId/runs/:runId"
          element={<div>run view</div>}
        />
      </Routes>
    </MemoryRouter>,
  );
}

function defaultGets(components: unknown[]) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}/workflow")
      return Promise.resolve({ data: { components }, error: undefined });
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

describe("WorkflowBuilder", () => {
  it("renders nodes loaded from the workflow", async () => {
    defaultGets([release, apply]);
    renderBuilder();
    expect(await screen.findByText("release")).toBeInTheDocument();
    expect(screen.getByText("apply")).toBeInTheDocument();
  });

  it("Save PUTs the assembled components with depends_on derived from edges", async () => {
    defaultGets([release, apply]);
    mockApi.PUT.mockResolvedValue({ data: { components: [] }, error: undefined });
    renderBuilder();
    await screen.findByText("release");

    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(mockApi.PUT).toHaveBeenCalled());
    const body = mockApi.PUT.mock.calls[0][1].body as {
      components: { id: string; depends_on: string[] }[];
    };
    const sent = Object.fromEntries(body.components.map((c) => [c.id, c]));
    // The dependent's depends_on is derived from the loaded edge release->apply.
    expect(sent[apply.id].depends_on).toEqual([release.id]);
    expect(sent[release.id].depends_on).toEqual([]);
    // Positions are persisted.
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
    renderBuilder();
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
    renderBuilder();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /deploy/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy" } }),
    );
  });

  it("shows an in-progress message on a 409", async () => {
    defaultGets([release]);
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderBuilder();
    await screen.findByText("release");
    await userEvent.click(screen.getByRole("button", { name: /deploy/i }));
    expect(
      await screen.findByText(/a run is already in progress/i),
    ).toBeInTheDocument();
  });

  it("adds a node via the Helm toolbar button", async () => {
    defaultGets([]);
    renderBuilder();
    await screen.findByText(/no components yet/i);
    await userEvent.click(screen.getByRole("button", { name: /^helm$/i }));
    // The new node appears with its default name, and the config panel opens.
    expect(await screen.findByText("helm release")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /delete node/i })).toBeInTheDocument();
  });
});
