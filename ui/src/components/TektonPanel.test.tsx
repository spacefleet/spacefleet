import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TektonPanel } from "./TektonPanel";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
  authToken: vi.fn(),
  currentOrgId: vi.fn(),
}));

// The embedded readiness report is exercised elsewhere; stub it so this test
// focuses on the install lifecycle, and stub the stream hook so no real SSE
// connection is opened.
vi.mock("./ClusterCapabilities", () => ({
  ClusterCapabilities: () => <div>capabilities</div>,
}));
vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(() => ({ value: null, status: "connecting", error: null })),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

function status(overrides: Record<string, unknown> = {}) {
  return {
    enabled: false,
    status: "not_installed",
    present: false,
    controller_ready: false,
    pinned_version: "v0.68.0",
    ...overrides,
  };
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
});

describe("TektonPanel", () => {
  it("shows not-set-up status and turns on job running via the switch", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    mockApi.POST.mockResolvedValue({
      data: status({ enabled: true, status: "installing", status_message: "queued for install" }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    const sw = await screen.findByRole("switch", {
      name: /Run jobs on this cluster/,
    });
    expect(sw).toHaveAttribute("aria-checked", "false");
    expect(mockApi.GET).toHaveBeenCalledWith("/api/clusters/{id}/tekton", {
      params: { path: { id: "c1" } },
    });
    await userEvent.click(sw);
    expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/tekton/enable", {
      params: { path: { id: "c1" } },
    });
    expect(await screen.findByText("Setting up job running…")).toBeInTheDocument();
  });

  it("shows an installed, job-running cluster with its engine details", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    expect(
      await screen.findByRole("switch", { name: /Run jobs on this cluster/ }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("Tekton v0.68.0")).toBeInTheDocument();
    // There is no longer a run-a-job action in the panel.
    expect(
      screen.queryByRole("button", { name: /Run a test job/ }),
    ).not.toBeInTheDocument();
  });

  it("reflects live install progress from the stream", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({ enabled: true, status: "installing" }),
      error: undefined,
    });
    const { rerender } = render(<TektonPanel clusterId="c1" canEdit />);
    // Initial load lands first (as in production: GET, then the stream connects).
    await screen.findByText("Setting up job running…");

    // A live progress update arrives over the stream.
    mockStream.mockReturnValue({
      value: status({
        enabled: true,
        status: "installing",
        status_message: "applied CustomResourceDefinition/taskruns.tekton.dev",
      }),
      status: "live",
      error: null,
    });
    rerender(<TektonPanel clusterId="c1" canEdit />);

    expect(
      await screen.findByText(
        "applied CustomResourceDefinition/taskruns.tekton.dev",
      ),
    ).toBeInTheDocument();
  });

  it("disables the switch for viewers", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    render(<TektonPanel clusterId="c1" canEdit={false} />);
    expect(
      await screen.findByRole("switch", { name: /Run jobs on this cluster/ }),
    ).toBeDisabled();
  });

  it("offers Upgrade only when managed and a newer version is pinned", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.65.0",
        pinned_version: "v0.68.0",
      }),
      error: undefined,
    });
    mockApi.POST.mockResolvedValue({
      data: status({ status: "upgrading", managed: true, present: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Upgrade/ }),
    );
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/clusters/{id}/tekton/upgrade",
      { params: { path: { id: "c1" } } },
    );
  });

  it("hides Upgrade when the running version matches the pinned version", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.68.0",
        pinned_version: "v0.68.0",
      }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    // Wait for the loaded panel (the engine block renders once Tekton is present).
    await screen.findByText("Tekton v0.68.0");
    expect(
      screen.queryByRole("button", { name: /Upgrade/ }),
    ).not.toBeInTheDocument();
  });

  it("deletes a managed install behind a confirm step", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    mockApi.POST.mockResolvedValue({
      data: status({ status: "uninstalling", managed: true, present: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    // First click only arms the confirm — no request yet.
    await userEvent.click(
      await screen.findByRole("button", { name: /Remove Tekton/ }),
    );
    expect(mockApi.POST).not.toHaveBeenCalled();

    await userEvent.click(
      screen.getByRole("button", { name: /Confirm remove/ }),
    );
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/clusters/{id}/tekton/uninstall",
      { params: { path: { id: "c1" } } },
    );
  });

  it("offers neither Upgrade nor Delete for a Tekton installed outside Spacefleet", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        status: "installed",
        present: true,
        controller_ready: true,
        managed: false,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    await screen.findByText(/Installed outside Spacefleet/);
    expect(screen.getByText("Existing install")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Upgrade/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Remove Tekton/ }),
    ).not.toBeInTheDocument();
    // The version Spacefleet would install is irrelevant for an existing install.
    expect(screen.queryByText(/Spacefleet installs/)).not.toBeInTheDocument();
  });

  it("labels an existing install as such even when job running is off", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: false,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: false,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    await screen.findByText("Existing install");
    expect(
      screen.getByRole("switch", { name: /Run jobs on this cluster/ }),
    ).toHaveAttribute("aria-checked", "false");
  });

  it("labels a Spacefleet-managed install", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    expect(await screen.findByText("Managed by Spacefleet")).toBeInTheDocument();
  });
});
