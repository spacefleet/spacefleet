import { render, screen, waitFor, within } from "@testing-library/react";
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

const target = { id: "cluster-1", name: "prod", runs_jobs: false };
const runner = { id: "cluster-2", name: "ci", runs_jobs: true };

const existingApp = {
  id: "app-1",
  name: "web",
  chart_source: "http_repo",
  status: "deployed",
  config: { repo_url: "https://charts.example.com", chart: "nginx", version: "1.0.0" },
  values: "replicas: 2\n",
  release_name: "web-rel",
  target_namespace: "apps",
  target_cluster_id: "cluster-1",
  runner_cluster_id: "cluster-2",
  values_sources: [
    { repo_url: "https://github.com/org/cfg.git", path: "prod/values.yaml", git_ref: "main" },
  ],
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
      return Promise.resolve({ data: [target, runner], error: undefined });
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
  it("creates an application then kicks off the first rollout and opens it", async () => {
    mockApi.POST.mockImplementation((path: string) => {
      if (path === "/api/applications")
        return Promise.resolve({ data: { id: "app-9" }, error: undefined });
      // The fire-and-forget rollout enqueue.
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderCreate();
    const user = userEvent.setup();

    expect(
      await screen.findByRole("heading", { name: "New Helm application" }),
    ).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText("my-app"), "checkout");
    await user.type(
      screen.getByPlaceholderText("https://charts.bitnami.com/bitnami"),
      "https://charts.example.com",
    );
    await user.type(screen.getByPlaceholderText("nginx"), "redis");
    await user.type(screen.getByPlaceholderText("default"), "shop");

    const targetSel = screen
      .getByRole("option", { name: "Select a cluster…" })
      .closest("select") as HTMLSelectElement;
    await user.selectOptions(targetSel, "cluster-1");
    const runnerSel = screen
      .getByRole("option", { name: "Select a runner…" })
      .closest("select") as HTMLSelectElement;
    await user.selectOptions(runnerSel, "cluster-2");

    await user.click(screen.getByRole("button", { name: "Create application" }));

    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications",
        expect.objectContaining({
          body: expect.objectContaining({
            name: "checkout",
            chart_source: "http_repo",
            target_namespace: "shop",
            target_cluster_id: "cluster-1",
            runner_cluster_id: "cluster-2",
            config: expect.objectContaining({
              repo_url: "https://charts.example.com",
              chart: "redis",
            }),
          }),
        }),
      ),
    );
    // The first rollout is enqueued (deploy) after creation.
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications/{id}/rollout",
        expect.objectContaining({ body: { action: "deploy" } }),
      ),
    );
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith("/applications/app-9"),
    );
  });

  it("surfaces a create error without enqueuing a rollout", async () => {
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "name taken" },
    });
    renderCreate();
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "New Helm application" });

    await user.type(screen.getByPlaceholderText("my-app"), "checkout");
    await user.type(
      screen.getByPlaceholderText("https://charts.bitnami.com/bitnami"),
      "https://charts.example.com",
    );
    await user.type(screen.getByPlaceholderText("nginx"), "redis");
    await user.type(screen.getByPlaceholderText("default"), "shop");
    await user.selectOptions(
      screen.getByRole("option", { name: "Select a cluster…" }).closest("select")!,
      "cluster-1",
    );
    await user.selectOptions(
      screen.getByRole("option", { name: "Select a runner…" }).closest("select")!,
      "cluster-2",
    );
    await user.click(screen.getByRole("button", { name: "Create application" }));

    expect(await screen.findByText("name taken")).toBeInTheDocument();
    // The rollout enqueue is never reached on a create failure.
    expect(mockApi.POST).not.toHaveBeenCalledWith(
      "/api/applications/{id}/rollout",
      expect.anything(),
    );
    expect(navigate).not.toHaveBeenCalled();
  });

  it("only offers job-running clusters as the runner", async () => {
    renderCreate();
    await screen.findByRole("heading", { name: "New Helm application" });
    const runnerSel = screen
      .getByRole("option", { name: "Select a runner…" })
      .closest("select") as HTMLSelectElement;
    // ci runs jobs; prod doesn't, so it's excluded from the runner list.
    expect(within(runnerSel).getByRole("option", { name: "ci" })).toBeInTheDocument();
    expect(
      within(runnerSel).queryByRole("option", { name: "prod" }),
    ).not.toBeInTheDocument();
  });

  it("redirects viewers away from the form", async () => {
    role = "viewer";
    render(
      <MemoryRouter initialEntries={["/applications/new"]}>
        <Routes>
          <Route path="/applications/new" element={<ApplicationForm />} />
          <Route path="/applications" element={<div>apps list</div>} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("apps list")).toBeInTheDocument();
  });
});

describe("ApplicationForm edit mode", () => {
  beforeEach(() => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters")
        return Promise.resolve({ data: [target, runner], error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: existingApp, error: undefined });
      return Promise.resolve({ data: [], error: undefined });
    });
  });

  it("hydrates from the existing application and locks fixed fields", async () => {
    renderEdit();
    expect(
      await screen.findByRole("heading", { name: "Edit web" }),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue("web")).toBeInTheDocument();
    expect(screen.getByDisplayValue("replicas: 2")).toBeInTheDocument();
    // Chart source picker is omitted when editing (fixed at registration).
    expect(
      screen.queryByRole("option", { name: "OCI registry" }),
    ).not.toBeInTheDocument();
    // The target/runner clusters are shown read-only by their name.
    expect(screen.getByText("prod")).toBeInTheDocument();
    expect(screen.getByText("ci")).toBeInTheDocument();
    // The existing values source is hydrated into the editor.
    expect(
      screen.getByDisplayValue("https://github.com/org/cfg.git"),
    ).toBeInTheDocument();
  });

  it("PATCHes the editable fields and returns to the detail page", async () => {
    mockApi.PATCH.mockResolvedValue({ data: existingApp, error: undefined });
    renderEdit();
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Edit web" });

    const name = screen.getByDisplayValue("web");
    await user.clear(name);
    await user.type(name, "web2");

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(mockApi.PATCH).toHaveBeenCalledWith(
        "/api/applications/{id}",
        expect.objectContaining({
          params: { path: { id: "app-1" } },
          body: expect.objectContaining({
            name: "web2",
            target_namespace: "apps",
            // The hydrated values source is sent back unchanged.
            values_sources: [
              {
                repo_url: "https://github.com/org/cfg.git",
                path: "prod/values.yaml",
                git_ref: "main",
              },
            ],
          }),
        }),
      ),
    );
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith("/applications/app-1"),
    );
  });

  it("shows a load error when the application can't be fetched", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/clusters")
        return Promise.resolve({ data: [target, runner], error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: undefined, error: { message: "gone" } });
      return Promise.resolve({ data: [], error: undefined });
    });
    renderEdit();
    expect(await screen.findByText("gone")).toBeInTheDocument();
  });
});

describe("ApplicationForm values-source editor", () => {
  it("adds, edits, and removes git values sources", async () => {
    renderCreate();
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "New Helm application" });

    // Starts empty.
    expect(
      screen.getByText(/No git values sources/),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /add values source/i }));
    await user.type(
      screen.getByLabelText("Source 1 repository URL"),
      "https://github.com/org/cfg.git",
    );
    await user.type(
      screen.getByLabelText("Source 1 values file path"),
      "prod/values.yaml",
    );
    await user.type(screen.getByLabelText("Source 1 branch or tag"), "main");

    // Add a second source, then remove the first.
    await user.click(screen.getByRole("button", { name: /add values source/i }));
    expect(screen.getByLabelText("Source 2 repository URL")).toBeInTheDocument();

    const firstRemove = screen.getAllByRole("button", { name: "Remove" })[0];
    await user.click(firstRemove);

    // The remaining (originally second, now re-labeled) source is the empty one;
    // the first source's data is gone.
    expect(
      screen.queryByDisplayValue("https://github.com/org/cfg.git"),
    ).not.toBeInTheDocument();
    expect(screen.getByLabelText("Source 1 repository URL")).toHaveValue("");
  });

  it("trims a git values source before sending it on create", async () => {
    mockApi.POST.mockResolvedValue({ data: { id: "app-9" }, error: undefined });
    renderCreate();
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "New Helm application" });

    await user.type(screen.getByPlaceholderText("my-app"), "checkout");
    await user.type(
      screen.getByPlaceholderText("https://charts.bitnami.com/bitnami"),
      "https://charts.example.com",
    );
    await user.type(screen.getByPlaceholderText("nginx"), "redis");
    await user.type(screen.getByPlaceholderText("default"), "shop");
    await user.selectOptions(
      screen.getByRole("option", { name: "Select a cluster…" }).closest("select")!,
      "cluster-1",
    );
    await user.selectOptions(
      screen.getByRole("option", { name: "Select a runner…" }).closest("select")!,
      "cluster-2",
    );

    // One source whose repo URL has surrounding whitespace to be trimmed.
    await user.click(screen.getByRole("button", { name: /add values source/i }));
    await user.type(
      screen.getByLabelText("Source 1 repository URL"),
      "  https://github.com/org/cfg.git  ",
    );
    await user.type(
      screen.getByLabelText("Source 1 values file path"),
      "prod/values.yaml",
    );

    await user.click(screen.getByRole("button", { name: "Create application" }));

    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications",
        expect.objectContaining({
          body: expect.objectContaining({
            values_sources: [
              {
                repo_url: "https://github.com/org/cfg.git",
                path: "prod/values.yaml",
              },
            ],
          }),
        }),
      ),
    );
  });
});
