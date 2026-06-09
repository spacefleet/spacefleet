import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationForm } from "./ApplicationForm";
import { api } from "../api/client";

const navigate = vi.fn();

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn() },
}));

let role = "owner";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();
  return { ...actual, useNavigate: () => navigate };
});

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  PATCH: ReturnType<typeof vi.fn>;
};

const runner = { id: "cluster-2", name: "ci" };

const existingApp = {
  id: "app-1",
  name: "web",
  imported: false,
  runner_cluster_id: "cluster-2",
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

beforeEach(() => {
  role = "owner";
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.PATCH.mockReset();
  navigate.mockReset();
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/clusters")
      return Promise.resolve({ data: [runner], error: undefined });
    return Promise.resolve({ data: [], error: undefined });
  });
});

function renderCreate() {
  return render(
    <MemoryRouter initialEntries={["/applications/new"]}>
      <Routes>
        <Route path="/applications/new" element={<ApplicationForm />} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderEdit() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/edit"]}>
      <Routes>
        <Route path="/applications/:appId/edit" element={<ApplicationForm />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ApplicationForm create mode", () => {
  it("creates an application then opens its workflow canvas", async () => {
    mockApi.POST.mockResolvedValue({ data: { id: "app-9" }, error: undefined });
    renderCreate();

    await userEvent.type(await screen.findByLabelText("Name"), "api");
    await userEvent.selectOptions(
      screen.getByLabelText("Runner cluster"),
      "cluster-2",
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/applications", {
        body: {
          name: "api",
          runner_cluster_id: "cluster-2",
        },
      }),
    );
    expect(navigate).toHaveBeenCalledWith("/applications/app-9/workflow");
  });

  it("surfaces a create error", async () => {
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "boom" },
    });
    renderCreate();

    await userEvent.type(await screen.findByLabelText("Name"), "api");
    await userEvent.selectOptions(
      screen.getByLabelText("Runner cluster"),
      "cluster-2",
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
  });
});

describe("ApplicationForm edit mode", () => {
  it("hydrates from the existing application and locks the runner", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters")
        return Promise.resolve({ data: [runner], error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: existingApp, error: undefined });
      return Promise.resolve({ data: [], error: undefined });
    });
    renderEdit();

    expect(await screen.findByDisplayValue("web")).toBeInTheDocument();
    // The runner is fixed at registration → shown read-only, not as a select.
    expect(screen.queryByLabelText("Runner cluster")).toBeNull();
    expect((await screen.findAllByText(/fixed at registration/)).length).toBe(1);
  });

  it("PATCHes the editable fields and returns to the detail page", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters")
        return Promise.resolve({ data: [runner], error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: existingApp, error: undefined });
      return Promise.resolve({ data: [], error: undefined });
    });
    mockApi.PATCH.mockResolvedValue({ data: existingApp, error: undefined });
    renderEdit();

    const name = await screen.findByDisplayValue("web");
    await userEvent.clear(name);
    await userEvent.type(name, "web2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mockApi.PATCH).toHaveBeenCalledWith("/api/applications/{id}", {
        params: { path: { id: "app-1" } },
        body: { name: "web2" },
      }),
    );
    expect(navigate).toHaveBeenCalledWith("/applications/app-1");
  });
});
