import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Notifications } from "./Notifications";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
}));

let role = "admin";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  PATCH: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const slack = {
  id: "ch-1",
  name: "ops-slack",
  kind: "slack",
  address: "hooks.slack.com",
  events: ["run_failed", "drift_detected"],
  application_id: "app-1",
  created_at: "2026-09-08T00:00:00Z",
  updated_at: "2026-09-08T00:00:00Z",
};

function mockLists(channels: unknown[]) {
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/notification-channels")
      return Promise.resolve({ data: channels, error: undefined });
    if (path === "/api/applications")
      return Promise.resolve({ data: [{ id: "app-1", name: "web" }], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/admin/notifications"]}>
      <Notifications />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  role = "admin";
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.PATCH.mockReset();
  mockApi.DELETE.mockReset();
});

describe("Notifications", () => {
  it("lists channels with their kind, destination, events, and scope", async () => {
    mockLists([slack]);
    renderPage();
    expect(await screen.findByText("ops-slack")).toBeInTheDocument();
    expect(screen.getByText("Slack")).toBeInTheDocument();
    expect(screen.getByText("hooks.slack.com")).toBeInTheDocument();
    expect(screen.getByText("Run failed, Drift detected")).toBeInTheDocument();
    expect(screen.getByText("web")).toBeInTheDocument();
  });

  it("hides the admin controls from an editor", async () => {
    role = "editor";
    mockLists([slack]);
    renderPage();
    await screen.findByText("ops-slack");
    expect(screen.queryByRole("button", { name: "Add channel" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete ops-slack" })).not.toBeInTheDocument();
  });

  it("adds a channel and sends a test", async () => {
    mockLists([]);
    mockApi.POST.mockImplementation((path: string) => {
      if (path === "/api/notification-channels")
        return Promise.resolve({
          data: { ...slack, id: "ch-2", name: "ops", kind: "email", address: "ops@example.com", events: ["run_failed"], application_id: null },
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: undefined, response: { status: 202 } });
    });
    renderPage();
    await screen.findByText("No channels yet");
    fireEvent.click(screen.getByRole("button", { name: "Add channel" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "ops" } });
    fireEvent.change(screen.getByLabelText("Email address"), { target: { value: "ops@example.com" } });
    // Drop two of the three default events.
    fireEvent.click(screen.getByLabelText("Awaiting approval"));
    fireEvent.click(screen.getByLabelText("Drift detected"));
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/notification-channels", {
        body: { name: "ops", kind: "email", target: "ops@example.com", events: ["run_failed"] },
      }),
    );
    expect(await screen.findByText("ops@example.com")).toBeInTheDocument();
    expect(screen.getByText("all applications")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Test ops" }));
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/notification-channels/{id}/test", {
        params: { path: { id: "ch-2" } },
      }),
    );
    expect(await screen.findByText("Test notification sent to ops.")).toBeInTheDocument();
  });

  it("adds a signed webhook and marks it in the list", async () => {
    mockLists([]);
    mockApi.POST.mockResolvedValue({
      data: { ...slack, id: "ch-3", name: "pager", kind: "webhook", address: "example.com", has_secret: true, application_id: null },
      error: undefined,
    });
    renderPage();
    await screen.findByText("No channels yet");
    fireEvent.click(screen.getByRole("button", { name: "Add channel" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "pager" } });
    expect(screen.queryByLabelText("Signing secret")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "webhook" } });
    fireEvent.change(screen.getByLabelText("Webhook URL"), { target: { value: "https://example.com/hooks/sf" } });
    fireEvent.change(screen.getByLabelText("Signing secret"), { target: { value: " s3cret " } });
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith("/api/notification-channels", {
        body: {
          name: "pager",
          kind: "webhook",
          target: "https://example.com/hooks/sf",
          events: ["awaiting_approval", "run_failed", "drift_detected"],
          secret: "s3cret",
        },
      }),
    );
    expect(await screen.findByText("example.com")).toBeInTheDocument();
    expect(screen.getByText("signed")).toBeInTheDocument();
  });

  it("rotates and removes a webhook's signing secret", async () => {
    const hook = { ...slack, id: "ch-3", name: "pager", kind: "webhook", address: "example.com", has_secret: false, application_id: null };
    mockLists([slack, hook]);
    renderPage();
    await screen.findByText("pager");
    // Only a webhook channel offers signing.
    expect(screen.queryByRole("button", { name: "Signing secret for ops-slack" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Signing secret for pager" }));
    expect(screen.queryByRole("button", { name: "Stop signing" })).not.toBeInTheDocument();

    mockApi.PATCH.mockResolvedValueOnce({ data: { ...hook, has_secret: true }, error: undefined });
    fireEvent.change(screen.getByLabelText("New signing secret"), { target: { value: " n3w " } });
    fireEvent.click(screen.getByRole("button", { name: "Start signing" }));
    await waitFor(() =>
      expect(mockApi.PATCH).toHaveBeenCalledWith("/api/notification-channels/{id}", {
        params: { path: { id: "ch-3" } },
        body: { secret: "n3w" },
      }),
    );
    expect(await screen.findByText("signed")).toBeInTheDocument();

    // Now signed: the dialog offers to stop, which clears the secret.
    fireEvent.click(screen.getByRole("button", { name: "Signing secret for pager" }));
    mockApi.PATCH.mockResolvedValueOnce({ data: { ...hook, has_secret: false }, error: undefined });
    fireEvent.click(screen.getByRole("button", { name: "Stop signing" }));
    await waitFor(() =>
      expect(mockApi.PATCH).toHaveBeenLastCalledWith("/api/notification-channels/{id}", {
        params: { path: { id: "ch-3" } },
        body: { secret: "" },
      }),
    );
    await waitFor(() => expect(screen.queryByText("signed")).not.toBeInTheDocument());
  });

  it("shows the API's reason when a channel is refused", async () => {
    mockLists([]);
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "target must be an http(s) URL" },
      response: { status: 400 },
    });
    renderPage();
    await screen.findByText("No channels yet");
    fireEvent.click(screen.getByRole("button", { name: "Add channel" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "hook" } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "webhook" } });
    fireEvent.change(screen.getByLabelText("Webhook URL"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    expect(await screen.findByText("target must be an http(s) URL")).toBeInTheDocument();
  });
});
