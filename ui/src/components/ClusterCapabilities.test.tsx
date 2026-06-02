import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ClusterCapabilities } from "./ClusterCapabilities";
import { api } from "../api/client";

// The API client and org context are mocked so the component can be driven
// without a backend.
vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" } }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};

const report = {
  identity: {
    username: "system:serviceaccount:spacefleet:reader",
    uid: "abc",
    groups: ["system:serviceaccounts"],
  },
  capabilities: [
    {
      key: "view_nodes",
      area: "Observe",
      title: "View nodes",
      status: "allowed" as const,
      missing_rules: [],
    },
    {
      key: "view_pod_logs",
      area: "Observe",
      title: "View pod logs",
      status: "denied" as const,
      missing_rules: [
        {
          api_group: "",
          resource: "pods",
          subresource: "log",
          verb: "get",
          reason: "no RBAC rule",
        },
      ],
    },
    {
      key: "restart_workloads",
      area: "Operate",
      title: "Restart workloads",
      status: "denied" as const,
      missing_rules: [
        { api_group: "apps", resource: "deployments", verb: "patch" },
      ],
    },
  ],
};

const manifest =
  "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: spacefleet-access";

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
});

describe("ClusterCapabilities", () => {
  it("renders capabilities grouped by area with allowed/denied indicators", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);

    expect(await screen.findByText("View nodes")).toBeInTheDocument();
    // Area headers.
    expect(screen.getByText("Observe")).toBeInTheDocument();
    expect(screen.getByText("Operate")).toBeInTheDocument();
    // The resolved identity is surfaced.
    expect(
      screen.getByText("system:serviceaccount:spacefleet:reader"),
    ).toBeInTheDocument();
    // One allowed, two denied.
    expect(screen.getByText("Allowed")).toBeInTheDocument();
    expect(screen.getAllByText("Denied")).toHaveLength(2);

    expect(mockApi.GET).toHaveBeenCalledWith("/api/clusters/{id}/capabilities", {
      params: { path: { id: "c1" } },
    });
  });

  it("expands a denied capability to show its missing rules (no per-row YAML)", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);

    const denied = await screen.findByRole("button", {
      name: /View pod logs/,
    });
    // Collapsed: nothing shown yet.
    expect(screen.queryByText("Missing permissions")).not.toBeInTheDocument();

    await userEvent.click(denied);

    expect(screen.getByText("Missing permissions")).toBeInTheDocument();
    expect(screen.getByText(/get pods\/log/)).toBeInTheDocument();
    // The per-row YAML is gone — a manifest only appears after Generate RBAC.
    expect(screen.queryByText(/kind: ClusterRole/)).not.toBeInTheDocument();
  });

  it("disables Generate RBAC until a capability is checked", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    const generate = screen.getByRole("button", { name: "Generate RBAC" });
    expect(generate).toBeDisabled();

    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include View nodes" }),
    );
    expect(generate).toBeEnabled();
    expect(screen.getByText("1 capability selected.")).toBeInTheDocument();
  });

  it("generates one manifest for the selected capabilities", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    mockApi.POST.mockResolvedValue({ data: { manifest }, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    // Select an allowed and a denied capability — both are grantable.
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include View nodes" }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include Restart workloads" }),
    );

    await userEvent.click(screen.getByRole("button", { name: "Generate RBAC" }));

    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/clusters/{id}/capabilities/rbac",
      {
        params: { path: { id: "c1" } },
        body: { keys: ["view_nodes", "restart_workloads"] },
      },
    );
    expect(await screen.findByText(/kind: ClusterRole/)).toBeInTheDocument();
  });

  it("copies the generated manifest to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    mockApi.POST.mockResolvedValue({ data: { manifest }, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include View pod logs" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Generate RBAC" }));
    await screen.findByText(/kind: ClusterRole/);
    await userEvent.click(screen.getByRole("button", { name: "Copy" }));

    expect(writeText).toHaveBeenCalledWith(manifest);
    expect(await screen.findByText("Copied")).toBeInTheDocument();
  });

  it("clears a generated manifest when the selection changes", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    mockApi.POST.mockResolvedValue({ data: { manifest }, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include View nodes" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Generate RBAC" }));
    expect(await screen.findByText(/kind: ClusterRole/)).toBeInTheDocument();

    // Changing the selection invalidates the (now stale) manifest.
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include Restart workloads" }),
    );
    expect(screen.queryByText(/kind: ClusterRole/)).not.toBeInTheDocument();
  });

  it("shows an error when generation fails", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "cluster unreachable" },
    });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    await userEvent.click(
      screen.getByRole("checkbox", { name: "Include View nodes" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Generate RBAC" }));
    expect(await screen.findByText("cluster unreachable")).toBeInTheDocument();
  });

  it("re-checks when the Re-check action is clicked", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");
    expect(mockApi.GET).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: /Re-check/ }));
    expect(mockApi.GET).toHaveBeenCalledTimes(2);
  });

  it("shows an error when the check fails", async () => {
    mockApi.GET.mockResolvedValue({
      data: undefined,
      error: { message: "cluster unreachable" },
    });
    render(<ClusterCapabilities clusterId="c1" />);
    expect(await screen.findByText("cluster unreachable")).toBeInTheDocument();
  });

  it("only renders denied rows as expand toggles", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);
    await screen.findByText("View nodes");

    // The allowed capability is not a button (not expandable).
    expect(
      screen.queryByRole("button", { name: /View nodes/ }),
    ).not.toBeInTheDocument();
    const observe = screen.getByText("View nodes");
    expect(within(observe.closest("li")!).getByText("Allowed")).toBeInTheDocument();
  });
});
