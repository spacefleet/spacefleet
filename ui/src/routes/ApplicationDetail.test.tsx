import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationDetail } from "./ApplicationDetail";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
  // The status stream (useObjectStream) reads these once a rollout/refresh is in
  // flight; stub them so the connection is a no-op rather than an unhandled error.
  authToken: () => Promise.resolve(null),
  currentOrgId: () => "org-1",
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

  it("shows the diff in a confirmation dialog before upgrading", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/deployments")
        return Promise.resolve({ data: deployments, error: undefined });
      if (path === "/api/clusters")
        return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
      if (path === "/api/applications/{id}/diff")
        return Promise.resolve({
          data: {
            sync_status: "out_of_sync",
            diff: "apps, web, Deployment (apps) has changed:\n+ replicas: 3",
          },
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    mockApi.POST.mockResolvedValue({ data: { ...app, status: "deploying" }, error: undefined });

    renderDetail();

    // A deployed app offers Upgrade; clicking it opens the confirmation, which
    // loads and shows the diff — no rollout is fired yet.
    await userEvent.click(await screen.findByRole("button", { name: /upgrade/i }));
    expect(await screen.findByText(/has changed/)).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();

    // Confirming fires the rollout (force defaults off, so an ordinary deploy).
    await userEvent.click(screen.getByRole("button", { name: /confirm upgrade/i }));
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/rollout",
      expect.objectContaining({ body: { action: "upgrade", force: false } }),
    );
  });

  it("sends force when the force-roll checkbox is ticked", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/deployments")
        return Promise.resolve({ data: deployments, error: undefined });
      if (path === "/api/clusters")
        return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
      if (path === "/api/applications/{id}/diff")
        // In sync — exactly the case where forcing a roll is wanted.
        return Promise.resolve({ data: { sync_status: "synced" }, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    mockApi.POST.mockResolvedValue({ data: { ...app, status: "deploying" }, error: undefined });

    renderDetail();

    await userEvent.click(await screen.findByRole("button", { name: /upgrade/i }));
    await userEvent.click(await screen.findByRole("checkbox", { name: /force roll resources/i }));
    await userEvent.click(screen.getByRole("button", { name: /confirm upgrade/i }));
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/rollout",
      expect.objectContaining({ body: { action: "upgrade", force: true } }),
    );
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
