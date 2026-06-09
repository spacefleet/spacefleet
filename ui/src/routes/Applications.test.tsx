import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Applications } from "./Applications";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn() },
}));

// Default role is editor (can create); a viewer-specific test overrides this.
let role = "editor";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  PUT: ReturnType<typeof vi.fn>;
};

// The page loads apps and groups together (Promise.all); route GET by path so a
// test can set each independently. Anything not specified resolves empty.
function mockData(opts: {
  apps?: unknown[];
  groups?: unknown[];
  appsError?: { message: string };
}) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications")
      return Promise.resolve({
        data: opts.appsError ? undefined : (opts.apps ?? []),
        error: opts.appsError,
      });
    if (path === "/api/application-groups")
      return Promise.resolve({ data: opts.groups ?? [], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

function renderApps() {
  return render(
    <MemoryRouter initialEntries={["/applications"]}>
      <Routes>
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/new" element={<div>new form</div>} />
        <Route path="/applications/import" element={<div>import page</div>} />
        <Route
          path="/applications/groups/:groupId"
          element={<div>group detail</div>}
        />
        <Route path="/applications/:id" element={<div>app detail</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const oneApp = {
  id: "app-1",
  name: "web",
  imported: false,
  runner_cluster_id: "c1",
  group_id: null,
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

const oneGroup = {
  id: "g1",
  name: "Backend",
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

beforeEach(() => {
  role = "editor";
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.PUT.mockReset();
  mockApi.POST.mockResolvedValue({ data: undefined, error: undefined });
  mockApi.PUT.mockResolvedValue({ data: undefined, error: undefined });
});

describe("Applications list", () => {
  it("shows a loading state before the list resolves", async () => {
    // A never-resolving GET keeps the page in its loading branch.
    mockApi.GET.mockReturnValue(new Promise(() => {}));
    renderApps();
    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });

  it("shows the empty state when there are no applications", async () => {
    mockData({});
    renderApps();
    expect(await screen.findByText("No applications yet")).toBeInTheDocument();
  });

  it("renders an error when the list fails to load", async () => {
    mockData({ appsError: { message: "boom" } });
    renderApps();
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });

  it("renders registered applications with their origin", async () => {
    mockData({ apps: [oneApp] });
    renderApps();
    expect(await screen.findByText("web")).toBeInTheDocument();
    // The origin column reflects whether the app was created or imported.
    expect(screen.getByText("Created")).toBeInTheDocument();
  });

  it("navigates to the detail page when a row is clicked", async () => {
    mockData({ apps: [oneApp] });
    renderApps();
    await userEvent.click(await screen.findByText("web"));
    expect(await screen.findByText("app detail")).toBeInTheDocument();
  });

  it("creates a Helm app via the primary button", async () => {
    mockData({});
    renderApps();
    await screen.findByText("No applications yet");
    await userEvent.click(screen.getByRole("button", { name: /create app/i }));
    expect(await screen.findByText("new form")).toBeInTheDocument();
  });

  it("hides the create affordances for viewers", async () => {
    role = "viewer";
    mockData({});
    renderApps();
    await screen.findByText("No applications yet");
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /create app/i }),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /new group/i }),
    ).not.toBeInTheDocument();
  });
});

describe("Applications groups", () => {
  it("lists groups as folders with their app counts", async () => {
    mockData({
      groups: [oneGroup],
      apps: [{ ...oneApp, group_id: "g1" }],
    });
    renderApps();
    const folder = await screen.findByText("Backend");
    const row = folder.closest("tr")!;
    expect(within(row).getByText("1")).toBeInTheDocument();
  });

  it("partitions grouped apps out of the ungrouped table", async () => {
    mockData({
      groups: [oneGroup],
      apps: [
        { ...oneApp, id: "a1", name: "grouped", group_id: "g1" },
        { ...oneApp, id: "a2", name: "loose", group_id: null },
      ],
    });
    renderApps();
    expect(await screen.findByText("loose")).toBeInTheDocument();
    expect(screen.queryByText("grouped")).not.toBeInTheDocument();
  });

  it("navigates into a group when its folder is clicked", async () => {
    mockData({ groups: [oneGroup], apps: [] });
    renderApps();
    await userEvent.click(await screen.findByText("Backend"));
    expect(await screen.findByText("group detail")).toBeInTheDocument();
  });

  it("creates a group from the inline form", async () => {
    mockData({});
    renderApps();
    await screen.findByText("No applications yet");

    await userEvent.click(screen.getByRole("button", { name: /new group/i }));
    await userEvent.type(
      screen.getByPlaceholderText(/Backend services/),
      "Frontend",
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(mockApi.POST).toHaveBeenCalledWith("/api/application-groups", {
      body: { name: "Frontend" },
    });
  });

  it("moves an ungrouped app into a group via the row select", async () => {
    mockData({
      groups: [oneGroup],
      apps: [{ ...oneApp, id: "a2", name: "loose", group_id: null }],
    });
    renderApps();
    await screen.findByText("loose");
    await userEvent.selectOptions(screen.getByRole("combobox"), "g1");

    expect(mockApi.PUT).toHaveBeenCalledWith("/api/applications/{id}/group", {
      params: { path: { id: "a2" } },
      body: { group_id: "g1" },
    });
  });
});
