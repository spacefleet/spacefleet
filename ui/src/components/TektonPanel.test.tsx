import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TektonPanel } from "./TektonPanel";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn() },
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
  PUT: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

function status(overrides: Record<string, unknown> = {}) {
  return {
    enabled: false,
    status: "not_installed",
    present: false,
    controller_ready: false,
    managed: false,
    pinned_version: "v0.68.0",
    expected_revision: "abc123def456",
    update_available: false,
    ...overrides,
  };
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.PUT.mockReset();
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
});

describe("TektonPanel", () => {
  it("installs Tekton only after the install is confirmed in a dialog", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    mockApi.POST.mockResolvedValue({
      data: status({ enabled: true, status: "installing", status_message: "queued for install" }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Set up as runner" }),
    );
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(mockApi.GET).toHaveBeenCalledWith("/api/clusters/{id}/tekton", {
      params: { path: { id: "c1" } },
    });
    // The click only opens the dialog, which spells out the cluster-wide
    // footprint — nothing is sent yet.
    const dialog = screen.getByRole("dialog", {
      name: /Install Tekton and set up this runner/,
    });
    expect(within(dialog).getByText(/Tekton Pipelines v0\.68\.0/)).toBeInTheDocument();
    expect(within(dialog).getByText("spacefleet-jobs")).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();

    await userEvent.click(
      within(dialog).getByRole("button", { name: "Install Tekton" }),
    );
    expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/tekton/enable", {
      params: { path: { id: "c1" } },
    });
    expect(await screen.findByText("Setting up runner…")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("leaves the cluster untouched when the dialog is cancelled", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    render(<TektonPanel clusterId="c1" canEdit />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Set up as runner" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    // Escape closes it too.
    await userEvent.click(screen.getByRole("button", { name: "Set up as runner" }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("keeps the dialog open with the error when setup fails", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "cluster unreachable" },
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Set up as runner" }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Install Tekton" }),
    );
    expect(await within(dialog).findByText("cluster unreachable")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Install Tekton" })).toBeEnabled();
  });

  it("uses an existing install as a runner without installing anything", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        status: "installed",
        present: true,
        controller_ready: true,
        detected_version: "v0.68.0",
      }),
      error: undefined,
    });
    mockApi.POST.mockResolvedValue({
      data: status({ enabled: true, status: "installed", present: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Use as runner" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Use this cluster as a runner" });
    expect(within(dialog).getByText(/nothing new is installed/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Use as runner" }));
    expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/tekton/enable", {
      params: { path: { id: "c1" } },
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("stops using the cluster as a runner behind a dialog that says Tekton stays installed", async () => {
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
      data: status({ enabled: false, status: "installed", present: true, managed: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Stop using as runner" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Stop using this cluster as a runner" });
    expect(within(dialog).getByText(/Tekton stays installed/)).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop using as runner" }));
    expect(mockApi.POST).toHaveBeenCalledWith("/api/clusters/{id}/tekton/disable", {
      params: { path: { id: "c1" } },
    });
  });

  it("shows an installed runner cluster with its engine details", async () => {
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

    expect(await screen.findByText("On")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Stop using as runner" }),
    ).toBeInTheDocument();
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
    await screen.findByText("Setting up runner…");

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

  it("shows viewers the runner state without the button", async () => {
    mockApi.GET.mockResolvedValue({ data: status(), error: undefined });
    render(<TektonPanel clusterId="c1" canEdit={false} />);
    expect(await screen.findByText("Off")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /runner/ }),
    ).not.toBeInTheDocument();
  });

  it("offers Upgrade when the server reports a newer pinned version", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.65.0",
        pinned_version: "v0.68.0",
        update_available: true,
      }),
      error: undefined,
    });
    mockApi.POST.mockResolvedValue({
      data: status({ status: "upgrading", managed: true, present: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Upgrade to v0\.68\.0/ }),
    );
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/clusters/{id}/tekton/upgrade",
      { params: { path: { id: "c1" } } },
    );
  });

  it("offers Sync install when the install footprint changed at the same version", async () => {
    // Same Tekton version, but the server found a manifest-revision mismatch —
    // e.g. an install that predates a footprint change (or revision stamping).
    mockApi.GET.mockResolvedValue({
      data: status({
        enabled: true,
        status: "installed",
        present: true,
        controller_ready: true,
        managed: true,
        detected_version: "v0.68.0",
        pinned_version: "v0.68.0",
        update_available: true,
      }),
      error: undefined,
    });
    mockApi.POST.mockResolvedValue({
      data: status({ status: "upgrading", managed: true, present: true }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit />);
    expect(await screen.findByText("Update available")).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /Sync install/ }),
    );
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/clusters/{id}/tekton/upgrade",
      { params: { path: { id: "c1" } } },
    );
  });

  it("hides the update action when the install matches what Spacefleet expects", async () => {
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
      screen.queryByRole("button", { name: /Upgrade|Sync install/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Update available")).not.toBeInTheDocument();
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

  it("labels an existing install as such even when the cluster is not a runner", async () => {
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
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Use as runner" }),
    ).toBeInTheDocument();
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

  it("creates and removes the provider plugin cache", async () => {
    const installed = status({ enabled: true, status: "installed", present: true, controller_ready: true, managed: true, installed_version: "v0.68.0" });
    const withCache = { ...installed, plugin_cache: { size: "20Gi", storage_class: "nfs" } };
    mockApi.GET.mockResolvedValue({ data: installed, error: undefined });
    render(<TektonPanel clusterId="c1" canEdit />);

    await screen.findByText("Provider plugin cache");
    await userEvent.type(screen.getByLabelText("Plugin cache storage class"), "nfs");
    // The save response carries the new cache; the follow-up reload does too.
    mockApi.PUT.mockResolvedValueOnce({ data: withCache, error: undefined });
    mockApi.GET.mockResolvedValue({ data: withCache, error: undefined });
    await userEvent.click(screen.getByRole("button", { name: "Create cache" }));
    expect(mockApi.PUT).toHaveBeenCalledWith("/api/clusters/{id}/tekton/plugin-cache", {
      params: { path: { id: "c1" } },
      body: { size: "20Gi", storage_class: "nfs" },
    });
    expect(await screen.findByText("20Gi")).toBeInTheDocument();
    expect(screen.getByText("· nfs")).toBeInTheDocument();

    // Growing resizes in place, keeping the class; the same size is a no-op.
    const resize = screen.getByRole("button", { name: "Resize" });
    expect(resize).toBeDisabled();
    const grown = { ...installed, plugin_cache: { size: "50Gi", storage_class: "nfs" } };
    mockApi.PUT.mockResolvedValueOnce({ data: grown, error: undefined });
    mockApi.GET.mockResolvedValue({ data: grown, error: undefined });
    await userEvent.clear(screen.getByLabelText("New plugin cache size"));
    await userEvent.type(screen.getByLabelText("New plugin cache size"), "50Gi");
    await userEvent.click(resize);
    expect(mockApi.PUT).toHaveBeenLastCalledWith("/api/clusters/{id}/tekton/plugin-cache", {
      params: { path: { id: "c1" } },
      body: { size: "50Gi", storage_class: "nfs" },
    });
    expect(await screen.findByText("50Gi")).toBeInTheDocument();

    mockApi.PUT.mockResolvedValueOnce({ data: installed, error: undefined });
    mockApi.GET.mockResolvedValue({ data: installed, error: undefined });
    await userEvent.click(screen.getByRole("button", { name: "Remove cache" }));
    expect(mockApi.PUT).toHaveBeenLastCalledWith("/api/clusters/{id}/tekton/plugin-cache", {
      params: { path: { id: "c1" } },
      body: { size: "" },
    });
    expect(await screen.findByRole("button", { name: "Create cache" })).toBeInTheDocument();
  });

  it("shows the plugin cache read-only to viewers", async () => {
    mockApi.GET.mockResolvedValue({
      data: status({ enabled: true, status: "installed", present: true, controller_ready: true, plugin_cache: { size: "10Gi" } }),
      error: undefined,
    });
    render(<TektonPanel clusterId="c1" canEdit={false} />);
    expect(await screen.findByText("10Gi")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove cache" })).not.toBeInTheDocument();
  });
});
