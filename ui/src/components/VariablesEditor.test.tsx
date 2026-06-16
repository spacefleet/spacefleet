import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { VariablesEditor } from "./VariablesEditor";
import { stagedBackend } from "./variablesBackend";
import type { components } from "../api/schema";
import { api } from "../api/client";

type Variable = components["schemas"]["Variable"];

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  PATCH: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const nonSecret = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "LOG_LEVEL",
  sensitive: false,
  value: "debug",
  created_at: "2026-06-08T00:00:00Z",
  updated_at: "2026-06-08T00:00:00Z",
};
const secret = {
  id: "22222222-2222-2222-2222-222222222222",
  name: "API_KEY",
  sensitive: true,
  // The server never returns a sensitive value.
  created_at: "2026-06-08T00:00:00Z",
  updated_at: "2026-06-08T00:00:00Z",
};

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.PATCH.mockReset();
  mockApi.DELETE.mockReset();
});

describe("VariablesEditor", () => {
  it("shows non-secret values but never a sensitive value", async () => {
    mockApi.GET.mockResolvedValue({ data: [nonSecret, secret], error: undefined });
    render(
      <VariablesEditor scope={{ kind: "app", appId: "app-1" }} canEdit />,
    );

    expect(await screen.findByText("LOG_LEVEL")).toBeInTheDocument();
    expect(screen.getByText("debug")).toBeInTheDocument();
    // The sensitive one shows a "set" placeholder, not its value.
    expect(screen.getByText("API_KEY")).toBeInTheDocument();
    expect(screen.getByText(/\(set\)/)).toBeInTheDocument();
  });

  it("adds a variable via POST and appends it to the list", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    const created = {
      id: "33333333-3333-3333-3333-333333333333",
      name: "REGION",
      sensitive: false,
      value: "us-east-1",
      created_at: "2026-06-08T00:00:00Z",
      updated_at: "2026-06-08T00:00:00Z",
    };
    mockApi.POST.mockResolvedValue({ data: created, error: undefined });

    render(<VariablesEditor scope={{ kind: "app", appId: "app-1" }} canEdit />);
    await screen.findByText("No variables.");

    await userEvent.type(screen.getByLabelText("Variable name"), "REGION");
    await userEvent.type(screen.getByLabelText("Variable value"), "us-east-1");
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/variables",
      expect.objectContaining({
        body: { name: "REGION", sensitive: false, value: "us-east-1" },
      }),
    );
    expect(await screen.findByText("REGION")).toBeInTheDocument();
  });

  it("sends a component-scoped POST when the scope is a component", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    mockApi.POST.mockResolvedValue({
      data: { ...nonSecret, name: "X", value: "y" },
      error: undefined,
    });
    render(
      <VariablesEditor
        scope={{ kind: "component", appId: "app-1", componentId: "comp-9" }}
        canEdit
      />,
    );
    await screen.findByText("No variables.");
    await userEvent.type(screen.getByLabelText("Variable name"), "X");
    await userEvent.type(screen.getByLabelText("Variable value"), "y");
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/components/{componentId}/variables",
      expect.objectContaining({
        params: { path: { id: "app-1", componentId: "comp-9" } },
      }),
    );
  });

  it("sends a group-scoped POST when the scope is a group", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    mockApi.POST.mockResolvedValue({
      data: { ...nonSecret, name: "X", value: "y" },
      error: undefined,
    });
    render(
      <VariablesEditor scope={{ kind: "group", groupId: "grp-7" }} canEdit />,
    );
    await screen.findByText("No variables.");
    expect(mockApi.GET).toHaveBeenCalledWith(
      "/api/application-groups/{id}/variables",
      expect.objectContaining({ params: { path: { id: "grp-7" } } }),
    );
    await userEvent.type(screen.getByLabelText("Variable name"), "X");
    await userEvent.type(screen.getByLabelText("Variable value"), "y");
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/application-groups/{id}/variables",
      expect.objectContaining({ params: { path: { id: "grp-7" } } }),
    );
  });

  it("deletes a variable after confirmation", async () => {
    mockApi.GET.mockResolvedValue({ data: [nonSecret], error: undefined });
    mockApi.DELETE.mockResolvedValue({ data: undefined, error: undefined });
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<VariablesEditor scope={{ kind: "app", appId: "app-1" }} canEdit />);
    const row = (await screen.findByText("LOG_LEVEL")).closest("li")!;
    await userEvent.click(
      within(row).getByRole("button", { name: /delete log_level/i }),
    );

    await waitFor(() => expect(mockApi.DELETE).toHaveBeenCalled());
    await waitFor(() =>
      expect(screen.queryByText("LOG_LEVEL")).not.toBeInTheDocument(),
    );
  });

  it("stages variables in memory (no API call) with a custom backend", async () => {
    // The workflow editor passes a staged backend for a not-yet-saved component:
    // adds go to an in-memory list, never the API, and a sensitive value is held
    // for the later flush while still being masked in the row.
    let list: Variable[] = [];
    const backend = stagedBackend(
      () => list,
      (next) => {
        list = next;
      },
    );

    render(
      <VariablesEditor
        scope={{ kind: "component", appId: "app-1", componentId: "new" }}
        canEdit
        backend={backend}
      />,
    );
    await screen.findByText("No variables.");

    await userEvent.type(screen.getByLabelText("Variable name"), "API_KEY");
    await userEvent.type(screen.getByLabelText("Variable value"), "s3cr3t");
    await userEvent.click(screen.getByLabelText("Sensitive"));
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    // The row appears, masked, and nothing hit the network.
    expect(await screen.findByText("API_KEY")).toBeInTheDocument();
    expect(screen.getByText(/\(set\)/)).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();

    // The plaintext is retained on the staged row so the flush can POST it.
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({
      name: "API_KEY",
      sensitive: true,
      value: "s3cr3t",
    });
  });

  it("hides editing controls for viewers", async () => {
    mockApi.GET.mockResolvedValue({ data: [nonSecret], error: undefined });
    render(
      <VariablesEditor scope={{ kind: "app", appId: "app-1" }} canEdit={false} />,
    );
    await screen.findByText("LOG_LEVEL");
    expect(screen.queryByLabelText("Variable name")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^add$/i }),
    ).not.toBeInTheDocument();
  });
});
