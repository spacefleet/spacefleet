import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CloudCredentials } from "./CloudCredentials";
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

const awsCredential = {
  id: "cred-1",
  name: "prod-aws",
  provider: "aws",
  description: "billing",
  config: { region: "us-east-1" },
  created_at: "2026-06-03T00:00:00Z",
  updated_at: "2026-06-03T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/admin/cloud-credentials"]}>
      <CloudCredentials />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
});

describe("CloudCredentials", () => {
  it("shows the empty state when there are no credentials", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderPage();
    expect(
      await screen.findByText("No cloud credentials yet"),
    ).toBeInTheDocument();
  });

  it("renders credentials with their provider label and config summary", async () => {
    mockApi.GET.mockResolvedValue({ data: [awsCredential], error: undefined });
    renderPage();
    expect(await screen.findByText("prod-aws")).toBeInTheDocument();
    expect(screen.getByText("AWS")).toBeInTheDocument();
    expect(screen.getByText("us-east-1")).toBeInTheDocument();
    expect(screen.getByText("billing")).toBeInTheDocument();
  });

  it("creates an AWS credential through the dialog with the flat fields", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    mockApi.POST.mockResolvedValue({
      data: { ...awsCredential, name: "staging-aws" },
      error: undefined,
    });
    renderPage();
    await screen.findByText("No cloud credentials yet");

    await userEvent.click(
      screen.getByRole("button", { name: /add credential/i }),
    );
    await userEvent.type(
      screen.getByPlaceholderText("production-aws"),
      "staging-aws",
    );
    await userEvent.type(screen.getByLabelText("Access key ID"), "AKIA123");
    await userEvent.type(
      screen.getByLabelText("Secret access key"),
      "shhh",
    );
    const submit = screen
      .getAllByRole("button", { name: /add credential/i })
      .find((b) => b.getAttribute("type") === "submit")!;
    await userEvent.click(submit);

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    const [path, opts] = mockApi.POST.mock.calls[0];
    expect(path).toBe("/api/cloud-credentials");
    expect(opts.body).toMatchObject({
      name: "staging-aws",
      provider: "aws",
      aws_access_key_id: "AKIA123",
      aws_secret_access_key: "shhh",
    });
    expect(await screen.findByText("staging-aws")).toBeInTheDocument();
  });

  it("swaps the field set when the provider changes to Azure", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderPage();
    await screen.findByText("No cloud credentials yet");

    await userEvent.click(
      screen.getByRole("button", { name: /add credential/i }),
    );
    // AWS fields present initially.
    expect(screen.getByLabelText("Access key ID")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Provider"), "azure");

    // Azure fields now present, AWS gone.
    expect(screen.getByLabelText("Tenant ID")).toBeInTheDocument();
    expect(screen.getByLabelText("Client secret")).toBeInTheDocument();
    expect(screen.queryByLabelText("Access key ID")).not.toBeInTheDocument();
  });

  it("deletes a credential after confirmation", async () => {
    mockApi.GET.mockResolvedValue({ data: [awsCredential], error: undefined });
    mockApi.DELETE.mockResolvedValue({ error: undefined });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();

    await userEvent.click(await screen.findByLabelText("Delete prod-aws"));
    await waitFor(() =>
      expect(mockApi.DELETE).toHaveBeenCalledWith(
        "/api/cloud-credentials/{id}",
        { params: { path: { id: "cred-1" } } },
      ),
    );
    await waitFor(() =>
      expect(screen.queryByText("prod-aws")).not.toBeInTheDocument(),
    );
  });
});
