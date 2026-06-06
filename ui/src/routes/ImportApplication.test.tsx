import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ImportApplication } from "./ImportApplication";
import { api } from "../api/client";

const navigate = vi.fn();

vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "owner",
  }),
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();
  return { ...actual, useNavigate: () => navigate };
});

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

const cluster = { id: "cluster-1", name: "prod", runs_jobs: true };
const release = {
  name: "cache",
  namespace: "data",
  chart_name: "redis",
  chart_version: "1.2.3",
  status: "deployed",
  revision: 2,
  values: "replicas: 3\n",
  updated_at: "2026-06-01T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/applications/import"]}>
      <ImportApplication />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  navigate.mockReset();
});

describe("ImportApplication", () => {
  it("discovers releases on the selected cluster and lists them", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters") {
        return Promise.resolve({ data: [cluster], error: undefined });
      }
      return Promise.resolve({ data: [release], error: undefined });
    });

    renderPage();
    const user = userEvent.setup();

    await screen.findByRole("option", { name: "prod" });
    await user.selectOptions(screen.getByRole("combobox"), "cluster-1");
    await user.click(screen.getByRole("button", { name: "Discover" }));

    expect(await screen.findByText("cache")).toBeInTheDocument();
    expect(screen.getByText("redis:1.2.3")).toBeInTheDocument();
    // The releases call carried the chosen cluster id.
    expect(mockApi.GET).toHaveBeenCalledWith(
      "/api/clusters/{id}/releases",
      expect.objectContaining({
        params: expect.objectContaining({ path: { id: "cluster-1" } }),
      }),
    );
  });

  it("hands a chosen release to the form via router state on import", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters") {
        return Promise.resolve({ data: [cluster], error: undefined });
      }
      return Promise.resolve({ data: [release], error: undefined });
    });

    renderPage();
    const user = userEvent.setup();

    await screen.findByRole("option", { name: "prod" });
    await user.selectOptions(screen.getByRole("combobox"), "cluster-1");
    await user.click(screen.getByRole("button", { name: "Discover" }));
    await screen.findByText("cache");
    await user.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith("/applications/new", {
        state: { importSeed: { clusterId: "cluster-1", release } },
      }),
    );
  });

  it("shows the empty state when no releases are found", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters") {
        return Promise.resolve({ data: [cluster], error: undefined });
      }
      return Promise.resolve({ data: [], error: undefined });
    });

    renderPage();
    const user = userEvent.setup();

    await screen.findByRole("option", { name: "prod" });
    await user.selectOptions(screen.getByRole("combobox"), "cluster-1");
    await user.click(screen.getByRole("button", { name: "Discover" }));

    expect(await screen.findByText("No Helm releases found")).toBeInTheDocument();
  });
});
