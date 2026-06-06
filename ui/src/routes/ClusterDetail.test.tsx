import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ClusterDetail } from "./ClusterDetail";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

let role = "editor";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

// The capabilities report and Tekton panel each fetch their own data; stub them
// so this test isolates the ClusterDetail page's own loading/error/probe logic.
vi.mock("../components/ClusterCapabilities", () => ({
  ClusterCapabilities: () => <div>capabilities</div>,
}));
vi.mock("../components/TektonPanel", () => ({
  TektonPanel: () => <div>tekton</div>,
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const cluster = {
  id: "c1",
  name: "prod",
  connection_method: "eks",
  status: "connected",
  endpoint: "https://k8s.example.com",
  k8s_version: "v1.30.1",
  config: {},
  runs_jobs: false,
  created_at: "2026-05-30T00:00:00Z",
  updated_at: "2026-05-30T00:00:00Z",
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/admin/clusters/c1"]}>
      <Routes>
        <Route path="/admin/clusters/:clusterId" element={<ClusterDetail />} />
        <Route path="/admin/clusters" element={<div>clusters list</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  role = "editor";
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  // The on-load connectivity re-probe; default to a no-op echo of the cluster.
  mockApi.POST.mockResolvedValue({ data: cluster, error: undefined });
});

describe("ClusterDetail", () => {
  it("shows a loading state before the cluster resolves", () => {
    mockApi.GET.mockReturnValue(new Promise(() => {}));
    renderDetail();
    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });

  it("renders the cluster overview once loaded", async () => {
    mockApi.GET.mockResolvedValue({ data: cluster, error: undefined });
    renderDetail();

    expect(await screen.findByText("prod")).toBeInTheDocument();
    // Connection method is shown via its human label, not the raw enum.
    expect(screen.getByText("Amazon EKS")).toBeInTheDocument();
    expect(screen.getByText("v1.30.1")).toBeInTheDocument();
    expect(screen.getAllByText("https://k8s.example.com").length).toBeGreaterThan(0);
    // The mocked sub-panels render, confirming the loaded layout.
    expect(screen.getByText("capabilities")).toBeInTheDocument();
    expect(screen.getByText("tekton")).toBeInTheDocument();
  });

  it("re-probes connectivity on load", async () => {
    mockApi.GET.mockResolvedValue({ data: cluster, error: undefined });
    renderDetail();
    await screen.findByText("prod");
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/test", {
        params: { path: { id: "c1" } },
      }),
    );
  });

  it("shows a not-found / error card when the cluster fails to load", async () => {
    mockApi.GET.mockResolvedValue({
      data: undefined,
      error: { message: "Could not load this cluster" },
    });
    renderDetail();
    expect(
      await screen.findByText("Could not load this cluster"),
    ).toBeInTheDocument();
    // A way back is offered; no probe runs for an unloaded cluster.
    expect(screen.getByText("Return to clusters")).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("deletes the cluster and returns to the list", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mockApi.GET.mockResolvedValue({ data: cluster, error: undefined });
    mockApi.DELETE.mockResolvedValue({ data: undefined, error: undefined });
    renderDetail();
    await screen.findByText("prod");

    await userEvent.click(screen.getByRole("button", { name: /delete/i }));

    await waitFor(() =>
      expect(mockApi.DELETE).toHaveBeenCalledWith("/api/clusters/{id}", {
        params: { path: { id: "c1" } },
      }),
    );
    expect(await screen.findByText("clusters list")).toBeInTheDocument();
  });

  it("hides the delete action for viewers", async () => {
    role = "viewer";
    mockApi.GET.mockResolvedValue({ data: cluster, error: undefined });
    renderDetail();
    await screen.findByText("prod");
    expect(
      screen.queryByRole("button", { name: /delete/i }),
    ).not.toBeInTheDocument();
  });
});
