import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { RefAutocompleteField } from "./RefAutocompleteField";
import type { RefContext } from "./refAutocomplete";

const ctx: RefContext = {
  varsNames: ["LOG_LEVEL", "REGION"],
  componentNames: ["infra"],
  outputKeysByName: {
    infra: [{ key: "vpc_id", sensitive: false, type: '"string"' }],
  },
};

// Harness holds the controlled value, like the real ComponentFields field does.
function Harness({ disabled }: { disabled?: boolean }) {
  const [v, setV] = useState("");
  return (
    <RefAutocompleteField
      as="textarea"
      value={v}
      onChange={setV}
      context={ctx}
      disabled={disabled}
    />
  );
}

// open positions the caret at the end of `value` and fires the change so the
// component recomputes its suggestions — the same path real typing drives.
function open(el: HTMLTextAreaElement, value: string) {
  fireEvent.change(el, {
    target: { value, selectionStart: value.length, selectionEnd: value.length },
  });
}

describe("RefAutocompleteField", () => {
  it("offers namespaces inside an open reference and walks to a full ref", () => {
    render(<Harness />);
    const el = screen.getByRole("textbox") as HTMLTextAreaElement;

    open(el, "${{ ");
    expect(screen.getByText("vars")).toBeInTheDocument();
    expect(screen.getByText("run")).toBeInTheDocument();
    expect(screen.getByText("components")).toBeInTheDocument();

    // Pick the namespace, then a variable — the field fills in the whole ref.
    fireEvent.mouseDown(screen.getByText("vars"));
    expect(screen.getByText("LOG_LEVEL")).toBeInTheDocument();
    fireEvent.mouseDown(screen.getByText("LOG_LEVEL"));

    expect(el.value).toBe("${{ vars.LOG_LEVEL }}");
    // The completed reference closes the popup.
    expect(screen.queryByText("REGION")).not.toBeInTheDocument();
  });

  it("suggests known output keys for an upstream component", () => {
    render(<Harness />);
    const el = screen.getByRole("textbox") as HTMLTextAreaElement;
    open(el, "${{ components.infra.outputs.");
    expect(screen.getByText("vpc_id")).toBeInTheDocument();
    fireEvent.mouseDown(screen.getByText("vpc_id"));
    expect(el.value).toBe("${{ components.infra.outputs.vpc_id }}");
  });

  it("dismisses the popup on Escape", () => {
    render(<Harness />);
    const el = screen.getByRole("textbox") as HTMLTextAreaElement;
    open(el, "${{ ");
    expect(screen.getByText("vars")).toBeInTheDocument();
    fireEvent.keyDown(el, { key: "Escape" });
    expect(screen.queryByText("vars")).not.toBeInTheDocument();
  });

  it("never suggests when disabled", () => {
    render(<Harness disabled />);
    const el = screen.getByRole("textbox") as HTMLTextAreaElement;
    // A disabled field can't change, but even a forced recompute shows nothing.
    fireEvent.select(el, {
      target: { value: "${{ ", selectionStart: 4, selectionEnd: 4 },
    });
    expect(screen.queryByText("vars")).not.toBeInTheDocument();
  });
});
