import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Clusters } from "./Clusters";
import { api } from "../api/client";

// The API client and org context are mocked so we can drive the page's data
// without a backend.
vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" } }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

// Render the list inside a router, with a stand-in detail route so a row click
// can be observed landing on /providers/clusters/:clusterId.
function renderClusters() {
  return render(
    <MemoryRouter initialEntries={["/providers/clusters"]}>
      <Routes>
        <Route path="/providers/clusters" element={<Clusters />} />
        <Route
          path="/providers/clusters/:clusterId"
          element={<div>cluster detail</div>}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  // The page re-probes connectivity (POST .../test) for each cluster on load;
  // default it to a no-op so tests that don't care don't have to wire it.
  mockApi.POST.mockResolvedValue({ data: undefined, error: undefined });
});

const oneCluster = {
  id: "c1",
  name: "prod",
  connection_method: "eks",
  status: "connected",
  config: {},
  runs_jobs: false,
  k8s_version: "v1.30.1",
  created_at: "2026-05-30T00:00:00Z",
  updated_at: "2026-05-30T00:00:00Z",
};

describe("Clusters", () => {
  it("shows the empty state when there are no clusters", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderClusters();
    expect(await screen.findByText("No clusters yet")).toBeInTheDocument();
  });

  it("renders registered clusters with their status", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneCluster], error: undefined });
    renderClusters();
    expect(await screen.findByText("prod")).toBeInTheDocument();
    expect(screen.getByText("Amazon EKS")).toBeInTheDocument();
    expect(screen.getByText("connected")).toBeInTheDocument();
    expect(screen.getByText("v1.30.1")).toBeInTheDocument();
  });

  it("flags clusters that are designated to run jobs", async () => {
    mockApi.GET.mockResolvedValue({
      data: [
        oneCluster,
        { ...oneCluster, id: "c2", name: "ci", runs_jobs: true },
      ],
      error: undefined,
    });
    renderClusters();

    await screen.findByText("ci");
    // The job-enabled cluster is flagged; the other shows nothing for jobs.
    expect(screen.getByText("Jobs enabled")).toBeInTheDocument();
  });

  it("re-probes connectivity on load and drops the manual Test button", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneCluster], error: undefined });
    renderClusters();
    await screen.findByText("prod");

    // No confusing manual "Test" action — connectivity is checked automatically.
    expect(
      screen.queryByRole("button", { name: /Test/ }),
    ).not.toBeInTheDocument();
    // The background connectivity re-probe hit the test endpoint for the cluster.
    expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/test", {
      params: { path: { id: "c1" } },
    });
  });

  it("navigates to the cluster detail page when a row is clicked", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneCluster], error: undefined });
    renderClusters();

    await userEvent.click(await screen.findByText("prod"));

    // The stand-in detail route renders, proving the row linked to it.
    expect(await screen.findByText("cluster detail")).toBeInTheDocument();
  });

  it("opens the registration dialog with method options", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderClusters();
    await screen.findByText("No clusters yet");

    await userEvent.click(screen.getByRole("button", { name: "Add cluster" }));

    expect(
      screen.getByRole("heading", { name: "Add cluster" }),
    ).toBeInTheDocument();
    // The in-cluster method is selected by default and needs no credentials.
    expect(
      screen.getByText(/Spacefleet is running in this cluster/),
    ).toBeInTheDocument();
  });
});
