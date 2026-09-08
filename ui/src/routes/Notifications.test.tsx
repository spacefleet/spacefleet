import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Notifications } from "./Notifications";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

let role = "admin";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
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
