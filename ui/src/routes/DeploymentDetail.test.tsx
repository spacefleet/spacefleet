import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DeploymentDetail } from "./DeploymentDetail";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" } }),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

const app = {
  id: "app-1",
  name: "web",
  status: "deployed",
  last_run_name: "helm-web-zzz", // not this run, so no live streaming
  config: {},
};

const terminalRun = {
  id: "dep-1",
  application_id: "app-1",
  action: "deploy",
  status: "succeeded",
  message: "rolled out",
  run_name: "helm-web-abc",
  logs: "helm upgrade --install web\nrelease deployed\n",
  created_at: "2026-06-03T10:00:00Z",
  finished_at: "2026-06-03T10:02:30Z",
};

function renderRun() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/deployments/dep-1"]}>
      <Routes>
        <Route
          path="/applications/:appId/deployments/:deploymentId"
          element={<DeploymentDetail />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}/deployments/{deploymentId}")
      return Promise.resolve({ data: terminalRun, error: undefined });
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: app, error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

describe("DeploymentDetail", () => {
  it("renders a terminal run's status, duration, and persisted logs", async () => {
    renderRun();

    // 2m 30s elapsed between created_at and finished_at (unique on the page).
    expect(await screen.findByText("2m 30s")).toBeInTheDocument();
    // Status shows in both the badge and the detail row.
    expect(screen.getAllByText("succeeded").length).toBeGreaterThan(0);
    // The persisted logs are shown (this run is not the app's current run, so no
    // live stream is opened).
    expect(screen.getByText(/release deployed/)).toBeInTheDocument();
  });

  it("shows an error when the deployment can't be loaded", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/deployments/{deploymentId}")
        return Promise.resolve({
          data: undefined,
          error: { message: "deployment not found" },
        });
      return Promise.resolve({ data: app, error: undefined });
    });
    renderRun();
    expect(await screen.findByText("deployment not found")).toBeInTheDocument();
  });
});
