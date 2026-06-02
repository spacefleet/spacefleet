import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ClusterCapabilities } from "./ClusterCapabilities";
import { api } from "../api/client";

// The API client and org context are mocked so the component can be driven
// without a backend.
vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" } }),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

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
      // A scoped manifest granting just this capability.
      remediation:
        "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: spacefleet-restart-workloads",
    },
  ],
  remediation:
    "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: spacefleet-access",
};

beforeEach(() => {
  mockApi.GET.mockReset();
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

  it("expands a denied capability to show missing rules and remediation YAML", async () => {
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);

    const denied = await screen.findByRole("button", {
      name: /View pod logs/,
    });
    // Collapsed: remediation not visible yet.
    expect(screen.queryByText("How to enable")).not.toBeInTheDocument();

    await userEvent.click(denied);

    expect(screen.getByText("How to enable")).toBeInTheDocument();
    expect(screen.getByText(/get pods\/log/)).toBeInTheDocument();
    expect(screen.getByText(/kind: ClusterRole/)).toBeInTheDocument();
  });

  it("copies the remediation YAML to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: /View pod logs/ }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Copy" }));

    expect(writeText).toHaveBeenCalledWith(report.remediation);
    expect(await screen.findByText("Copied")).toBeInTheDocument();
  });

  it("prefers a capability's own scoped remediation over the report-level union", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    mockApi.GET.mockResolvedValue({ data: report, error: undefined });
    render(<ClusterCapabilities clusterId="c1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: /Restart workloads/ }),
    );
    // Shows the scoped role name, not the union role.
    expect(
      screen.getByText(/name: spacefleet-restart-workloads/),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(
      report.capabilities[2].remediation,
    );
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
