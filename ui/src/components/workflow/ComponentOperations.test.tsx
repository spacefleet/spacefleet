import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { ComponentOperations, type OperationPrefill } from "./ComponentOperations";

vi.mock("../../api/client", () => ({
  api: { POST: vi.fn() },
}));
const mockPost = api.POST as unknown as ReturnType<typeof vi.fn>;

function renderCard(prefill: OperationPrefill | null = null) {
  return render(
    <MemoryRouter initialEntries={["/node"]}>
      <Routes>
        <Route
          path="/node"
          element={
            <ComponentOperations appId="app-1" componentId="comp-1" prefill={prefill} />
          }
        />
        <Route path="/applications/:appId/runs/:runId" element={<div>run page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockPost.mockReset();
});

describe("ComponentOperations", () => {
  it("picks an operation first, then fills its form and starts it", async () => {
    mockPost.mockResolvedValue({
      data: { id: "run-42", action: "state_op", status: "pending" },
      error: undefined,
    });
    renderCard();
    // Step one: the four operations, and no form yet.
    const group = screen.getByRole("group", { name: "State operation" });
    expect(group.querySelectorAll("button")).toHaveLength(4);
    expect(screen.queryByRole("button", { name: "Start for approval" })).not.toBeInTheDocument();

    // Step two: the move's explanation and its two fields; the button stays
    // disabled until both are filled.
    fireEvent.click(screen.getByRole("button", { name: /Rename a resource/ }));
    expect(screen.queryByRole("group", { name: "State operation" })).not.toBeInTheDocument();
    expect(screen.getByText(/so a refactor/)).toBeInTheDocument();
    const start = screen.getByRole("button", { name: "Start for approval" });
    expect(start).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Current address"), { target: { value: "aws_instance.web" } });
    expect(start).toBeDisabled();
    fireEvent.change(screen.getByLabelText("New address"), { target: { value: " module.web.aws_instance.this " } });
    expect(start).toBeEnabled();
    fireEvent.click(start);

    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/state-ops",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: { operation: "mv", address: "aws_instance.web", new_address: "module.web.aws_instance.this" },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("goes back to the choice, clearing what was typed", () => {
    renderCard();
    fireEvent.click(screen.getByRole("button", { name: /Stop managing a resource/ }));
    fireEvent.change(screen.getByLabelText("Resource address"), { target: { value: "aws_instance.web" } });
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByRole("group", { name: "State operation" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Import existing infrastructure/ }));
    expect(screen.getByLabelText("Resource address")).toHaveValue("");
  });

  it("opens force-unlock filled in from the lock box", async () => {
    mockPost.mockResolvedValue({
      data: { id: "run-43", action: "state_op", status: "pending" },
      error: undefined,
    });
    renderCard({ kind: "force_unlock", values: { lock_id: "lock-1" } });
    expect(screen.getByText("Release a stuck state lock")).toBeInTheDocument();
    expect(screen.getByLabelText("Lock id")).toHaveValue("lock-1");
    fireEvent.click(screen.getByRole("button", { name: "Start for approval" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/state-ops",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: { operation: "force_unlock", lock_id: "lock-1" },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("shows the API's reason when an operation is refused", async () => {
    mockPost.mockResolvedValue({
      data: undefined,
      error: { message: "a run is already in progress for this application" },
      response: { status: 409 },
    });
    renderCard();
    fireEvent.click(screen.getByRole("button", { name: /Stop managing a resource/ }));
    fireEvent.change(screen.getByLabelText("Resource address"), { target: { value: "aws_instance.web" } });
    fireEvent.click(screen.getByRole("button", { name: "Start for approval" }));
    expect(
      await screen.findByText("a run is already in progress for this application"),
    ).toBeInTheDocument();
  });
});
