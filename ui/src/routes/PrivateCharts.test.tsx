import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PrivateCharts } from "./PrivateCharts";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "owner",
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const oneCredential = {
  id: "cred-1",
  name: "docker-hub",
  type: "oci",
  username: "robot",
  created_at: "2026-06-03T00:00:00Z",
  updated_at: "2026-06-03T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/admin/private-charts"]}>
      <PrivateCharts />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
});

describe("PrivateCharts", () => {
  it("shows the empty state when there are no credentials", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderPage();
    expect(
      await screen.findByText("No chart credentials yet"),
    ).toBeInTheDocument();
  });

  it("renders credentials with their type label and username", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneCredential], error: undefined });
    renderPage();
    expect(await screen.findByText("docker-hub")).toBeInTheDocument();
    expect(screen.getByText("OCI registry")).toBeInTheDocument();
    expect(screen.getByText("robot")).toBeInTheDocument();
  });

  it("creates a credential through the dialog and appends it to the list", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    mockApi.POST.mockResolvedValue({
      data: { ...oneCredential, name: "ghcr", type: "basic_auth" },
      error: undefined,
    });
    renderPage();
    await screen.findByText("No chart credentials yet");

    await userEvent.click(screen.getByRole("button", { name: /add credential/i }));
    await userEvent.type(screen.getByPlaceholderText("docker-hub"), "ghcr");
    await userEvent.type(screen.getByPlaceholderText("••••••••"), "token");
    // Both the page trigger and the dialog submit read "Add credential"; the
    // submit is the one of type=submit.
    const submit = screen
      .getAllByRole("button", { name: /add credential/i })
      .find((b) => b.getAttribute("type") === "submit")!;
    await userEvent.click(submit);

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    const [path, opts] = mockApi.POST.mock.calls[0];
    expect(path).toBe("/api/chart-credentials");
    expect(opts.body).toMatchObject({ name: "ghcr", password: "token" });
    expect(await screen.findByText("ghcr")).toBeInTheDocument();
  });

  it("deletes a credential after confirmation", async () => {
    mockApi.GET.mockResolvedValue({ data: [oneCredential], error: undefined });
    mockApi.DELETE.mockResolvedValue({ error: undefined });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();

    await userEvent.click(await screen.findByLabelText("Delete docker-hub"));
    await waitFor(() =>
      expect(mockApi.DELETE).toHaveBeenCalledWith("/api/chart-credentials/{id}", {
        params: { path: { id: "cred-1" } },
      }),
    );
    await waitFor(() =>
      expect(screen.queryByText("docker-hub")).not.toBeInTheDocument(),
    );
  });
});
