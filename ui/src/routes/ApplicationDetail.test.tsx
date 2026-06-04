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
  type: "helm",
  chart_source: "http_repo",
  status: "deployed",
  target_namespace: "apps",
  target_cluster_id: "c1",
  runner_cluster_id: "c1",
  last_run_name: "helm-web-2",
  config: {},
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

const deployments = [
  {
    id: "dep-2",
    application_id: "app-1",
    action: "upgrade",
    status: "succeeded",
    run_name: "helm-web-2",
    created_at: "2026-06-03T10:00:00Z",
    finished_at: "2026-06-03T10:01:00Z",
  },
  {
    id: "dep-1",
    application_id: "app-1",
    action: "deploy",
    status: "failed",
    run_name: "helm-web-1",
    created_at: "2026-06-03T09:00:00Z",
    finished_at: "2026-06-03T09:00:45Z",
  },
];

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1"]}>
      <Routes>
        <Route path="/applications/:appId" element={<ApplicationDetail />} />
        <Route
          path="/applications/:appId/deployments/:deploymentId"
          element={<div>run page</div>}
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
    if (path === "/api/applications/{id}/deployments")
      return Promise.resolve({ data: deployments, error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

describe("ApplicationDetail deployment history", () => {
  it("lists rollout runs newest-first with numbers and statuses", async () => {
    renderDetail();

    // Newest run is numbered highest (#2) and shows its action + status.
    expect(await screen.findByText("#2")).toBeInTheDocument();
    expect(screen.getByText("#1")).toBeInTheDocument();
    expect(screen.getByText("succeeded")).toBeInTheDocument();
    expect(screen.getByText("failed")).toBeInTheDocument();
  });

  it("opens the run page when a deployment row is clicked", async () => {
    renderDetail();
    await userEvent.click(await screen.findByText("#2"));
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("shows the empty state when there are no runs", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/deployments")
        return Promise.resolve({ data: [], error: undefined });
      return Promise.resolve({ data: [], error: undefined });
    });
    renderDetail();
    expect(await screen.findByText(/No rollouts yet/)).toBeInTheDocument();
  });
});
