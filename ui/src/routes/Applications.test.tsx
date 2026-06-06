import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Applications } from "./Applications";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

// Default role is editor (can create); a viewer-specific test overrides this.
let role = "editor";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

function renderApps() {
  return render(
    <MemoryRouter initialEntries={["/applications"]}>
      <Routes>
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/new" element={<div>new form</div>} />
        <Route path="/applications/import" element={<div>import page</div>} />
        <Route path="/applications/:id" element={<div>app detail</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const oneApp = {
  id: "app-1",
  name: "web",
  chart_source: "http_repo",
  status: "deployed",
  target_namespace: "apps",
  target_cluster_id: "c1",
  runner_cluster_id: "c1",
  config: {},
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

beforeEach(() => {
  role = "editor";
  mockApi.GET.mockReset();
});

describe("Applications list", () => {
  it("shows a loading state before the list resolves", async () => {
    // A never-resolving GET keeps the page in its loading branch.
    mockApi.GET.mockReturnValue(new Promise(() => {}));
    renderApps();
    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });

  it("shows the empty state when there are no applications", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderApps();
    expect(await screen.findByText("No applications yet")).toBeInTheDocument();
  });

  it("renders an error when the list fails to load", async () => {
    mockApi.GET.mockResolvedValue({
      data: undefined,
      error: { message: "boom" },
    });
    renderApps();
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });

  it("renders registered applications with source, namespace and status", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneApp], error: undefined });
    renderApps();
    expect(await screen.findByText("web")).toBeInTheDocument();
    expect(screen.getByText("HTTP Helm repository")).toBeInTheDocument();
    expect(screen.getByText("apps")).toBeInTheDocument();
    // The status badge text reflects the app status.
    expect(screen.getByText("deployed")).toBeInTheDocument();
  });

  it("navigates to the detail page when a row is clicked", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneApp], error: undefined });
    renderApps();
    await userEvent.click(await screen.findByText("web"));
    expect(await screen.findByText("app detail")).toBeInTheDocument();
  });

  it("creates a Helm app via the primary button", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderApps();
    await screen.findByText("No applications yet");
    await userEvent.click(screen.getByRole("button", { name: /create app/i }));
    expect(await screen.findByText("new form")).toBeInTheDocument();
  });

  it("offers Import existing release from the split menu", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderApps();
    await screen.findByText("No applications yet");
    await userEvent.click(
      screen.getByRole("button", { name: "More application types" }),
    );
    await userEvent.click(screen.getByText("Import existing release"));
    expect(await screen.findByText("import page")).toBeInTheDocument();
  });

  it("hides the create affordance for viewers", async () => {
    role = "viewer";
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderApps();
    await screen.findByText("No applications yet");
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /create app/i }),
      ).not.toBeInTheDocument(),
    );
  });
});
