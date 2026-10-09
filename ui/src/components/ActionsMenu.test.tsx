import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ActionsMenu } from "./ActionsMenu";

describe("ActionsMenu", () => {
  it("opens from the ellipsis button, runs the chosen action, and closes", async () => {
    const onEdit = vi.fn();
    render(
      <ActionsMenu
        label="web actions"
        items={[
          { label: "Edit", onSelect: onEdit },
          { label: "Delete", danger: true, onSelect: vi.fn() },
        ]}
      />,
    );
    expect(screen.queryByRole("menu")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "web actions" }));
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveClass(
      "text-red-700",
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));

    expect(onEdit).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("does not run a disabled action", async () => {
    const onMove = vi.fn();
    render(
      <ActionsMenu
        label="stage actions"
        items={[{ label: "Move left", disabled: true, onSelect: onMove }]}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "stage actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Move left" }));
    expect(onMove).not.toHaveBeenCalled();
  });

  it("keeps its clicks from reaching a clickable parent", async () => {
    const onRow = vi.fn();
    const onLogs = vi.fn();
    render(
      <div onClick={onRow}>
        <ActionsMenu
          label="Pod actions"
          items={[{ label: "View logs", onSelect: onLogs }]}
        />
      </div>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Pod actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "View logs" }));
    expect(onLogs).toHaveBeenCalledOnce();
    expect(onRow).not.toHaveBeenCalled();
  });
});
