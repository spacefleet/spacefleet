import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Policies } from "./Policies";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
}));

let role = "admin";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as Record<"GET" | "POST" | "PATCH" | "DELETE", ReturnType<typeof vi.fn>>;

const policy = {
  id: "pol-1",
  name: "no-db-deletes",
  description: "keep the databases",
  rego: "package spacefleet\n\ndeny contains msg if { false }\n",
  enforcement: "block",
  enabled: true,
  application_id: null,
  created_at: "2026-09-08T00:00:00Z",
  updated_at: "2026-09-08T00:00:00Z",
};

const plan = {
  component_run_id: "cr-1",
  run_id: "run-1",
  application_id: "app-1",
  application_name: "web",
  component_name: "infra",
  action: "deploy",
  finished_at: "2026-09-08T10:00:00Z",
  add: 1,
  change: 0,
  destroy: 1,
  replace: 0,
};

function mockLists(policies: unknown[], plans: unknown[] = []) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/policies") return Promise.resolve({ data: policies, error: undefined });
    if (path === "/api/policies/plans") return Promise.resolve({ data: plans, error: undefined });
    if (path === "/api/applications")
      return Promise.resolve({ data: [{ id: "app-1", name: "web" }], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/admin/policies"]}>
      <Policies />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  role = "admin";
  for (const m of Object.values(mockApi)) m.mockReset();
});

describe("Policies", () => {
  it("lists policies and toggles one", async () => {
    mockLists([policy]);
    mockApi.PATCH.mockResolvedValue({ data: { ...policy, enabled: false }, error: undefined });
    renderPage();
    expect(await screen.findByText("no-db-deletes")).toBeInTheDocument();
    expect(screen.getByText("keep the databases")).toBeInTheDocument();
    expect(screen.getByText("all applications")).toBeInTheDocument();
    const toggle = screen.getByLabelText("Enable no-db-deletes") as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(mockApi.PATCH).toHaveBeenCalledWith("/api/policies/{id}", {
        params: { path: { id: "pol-1" } },
        body: { enabled: false },
      }),
    );
    await waitFor(() => expect(toggle.checked).toBe(false));
  });

  it("adds a policy scoped to an application", async () => {
    mockLists([]);
    mockApi.POST.mockResolvedValue({
      data: { ...policy, id: "pol-2", name: "cap", enforcement: "warn", application_id: "app-1" },
      error: undefined,
    });
    renderPage();
    await screen.findByText("No policies yet");
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "cap" } });
    fireEvent.change(screen.getByLabelText("Enforcement"), { target: { value: "warn" } });
    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "app-1" } });
    fireEvent.change(screen.getByLabelText("Rego"), { target: { value: "package spacefleet\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/policies", {
        body: { name: "cap", description: "", rego: "package spacefleet\n", enforcement: "warn", application_id: "app-1" },
      }),
    );
    expect(await screen.findByText("cap")).toBeInTheDocument();
    expect(screen.getByText("web")).toBeInTheDocument();
  });

  it("tests the Rego as typed against a recent plan without saving", async () => {
    mockLists([], [plan]);
    mockApi.POST.mockImplementation((path: string) => {
      if (path === "/api/policies/test")
        return Promise.resolve({
          data: {
            violations: ["aws_db_instance.main would be destroyed"],
            input: { plan: { destroy: 1 } },
          },
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: { message: "unexpected" } });
    });
    renderPage();
    await screen.findByText("No policies yet");
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }));
    const picker = await screen.findByLabelText("Plan to test against");
    expect(picker).toHaveTextContent("web / infra · deploy · +1 ~0 -1");
    fireEvent.change(screen.getByLabelText("Rego"), { target: { value: "package spacefleet\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Test policy" }));
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/policies/test", {
        body: { rego: "package spacefleet\n", component_run_id: "cr-1" },
      }),
    );
    expect(await screen.findByText("1 violation")).toBeInTheDocument();
    expect(screen.getByText("aws_db_instance.main would be destroyed")).toBeInTheDocument();
    expect(screen.getByText("What the policy saw")).toBeInTheDocument();
    // Nothing was saved.
    expect(mockApi.POST).not.toHaveBeenCalledWith("/api/policies", expect.anything());
  });

  it("shows the compiler's message when the Rego is refused", async () => {
    mockLists([]);
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "policy: invalid policy: 1 error occurred: policy.rego:3: rego_parse_error" },
      response: { status: 400 },
    });
    renderPage();
    await screen.findByText("No policies yet");
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    expect(await screen.findByText(/rego_parse_error/)).toBeInTheDocument();
  });

  it("lets an editor read a policy without editing it", async () => {
    role = "editor";
    mockLists([policy]);
    renderPage();
    fireEvent.click(await screen.findByText("no-db-deletes"));
    expect(screen.getByLabelText("Rego")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Save policy" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add policy" })).not.toBeInTheDocument();
  });
});
