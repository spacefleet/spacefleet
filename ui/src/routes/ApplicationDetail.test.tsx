import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationDetail } from "./ApplicationDetail";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "editor",
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const app = {
  id: "app-1",
  name: "web",
  imported: false,
  target_namespace: "apps",
  target_cluster_id: "c1",
  runner_cluster_id: "c1",
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

const run = {
  id: "run-2",
  application_id: "app-1",
  action: "deploy",
  status: "succeeded",
  created_at: "2026-06-03T10:00:00Z",
  finished_at: "2026-06-03T10:01:00Z",
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1"]}>
      <Routes>
        <Route path="/applications/:appId" element={<ApplicationDetail />} />
        <Route
          path="/applications/:appId/workflow"
          element={<div>workflow canvas</div>}
        />
        <Route
          path="/applications/:appId/runs/:runId"
          element={<div>run view</div>}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: app, error: undefined });
    if (path === "/api/applications/{id}/runs")
      return Promise.resolve({ data: { runs: [run] }, error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({
        data: [{ id: "c1", name: "prod" }],
        error: undefined,
      });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

describe("ApplicationDetail overview", () => {
  it("shows the targeting and the latest run status", async () => {
    renderDetail();
    expect(await screen.findByText("web")).toBeInTheDocument();
    // The cluster id resolves to its name.
    expect((await screen.findAllByText("prod")).length).toBeGreaterThan(0);
    expect(screen.getByText("apps")).toBeInTheDocument();
    // The latest run's action + status are shown.
    expect(screen.getByText("deploy")).toBeInTheDocument();
    expect(screen.getByText("succeeded")).toBeInTheDocument();
  });

  it("links to the workflow builder", async () => {
    renderDetail();
    await userEvent.click(
      await screen.findByRole("button", { name: /workflow/i }),
    );
    expect(await screen.findByText("workflow canvas")).toBeInTheDocument();
  });

  it("opens the run view from the latest run", async () => {
    renderDetail();
    await userEvent.click(await screen.findByText("deploy"));
    expect(await screen.findByText("run view")).toBeInTheDocument();
  });

  it("shows the empty state when there are no runs", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/runs")
        return Promise.resolve({ data: { runs: [] }, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    expect(await screen.findByText(/No runs yet/)).toBeInTheDocument();
  });
});
