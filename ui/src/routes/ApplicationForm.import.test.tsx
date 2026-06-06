import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationForm } from "./ApplicationForm";
import { api } from "../api/client";

const navigate = vi.fn();

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn() },
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

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};

const cluster = { id: "cluster-1", name: "prod" };
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

function renderImport() {
  return render(
    <MemoryRouter
      initialEntries={[
        {
          pathname: "/applications/new",
          state: { importSeed: { clusterId: "cluster-1", release } },
        },
      ]}
    >
      <ApplicationForm />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  navigate.mockReset();
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/clusters") {
      return Promise.resolve({ data: [cluster], error: undefined });
    }
    return Promise.resolve({ data: [], error: undefined });
  });
});

describe("ApplicationForm import mode", () => {
  it("pre-fills the name, namespace, and target cluster from the release", async () => {
    renderImport();

    expect(
      await screen.findByRole("heading", { name: "Import application" }),
    ).toBeInTheDocument();
    // Name + namespace seeded from the live release.
    expect(screen.getByDisplayValue("cache")).toBeInTheDocument();
    expect(screen.getByDisplayValue("data")).toBeInTheDocument();
  });

  it("adopts via POST /api/applications/import then opens the workflow canvas", async () => {
    mockApi.POST.mockResolvedValue({ data: { id: "app-9" }, error: undefined });
    renderImport();
    const user = userEvent.setup();

    await screen.findByRole("heading", { name: "Import application" });

    // The cluster the release was found on is pre-selected as the target;
    // choose the runner cluster.
    await user.selectOptions(
      screen.getByLabelText("Runner cluster"),
      "cluster-1",
    );
    await user.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications/import",
        expect.objectContaining({
          body: expect.objectContaining({
            name: "cache",
            target_namespace: "data",
            target_cluster_id: "cluster-1",
            runner_cluster_id: "cluster-1",
          }),
        }),
      ),
    );
    // Adopt must not trigger a rollout.
    expect(mockApi.POST).not.toHaveBeenCalledWith(
      "/api/applications/{id}/rollout",
      expect.anything(),
    );
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith("/applications/app-9/workflow"),
    );
  });
});
