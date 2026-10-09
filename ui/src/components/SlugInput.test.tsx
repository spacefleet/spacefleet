import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { slugify } from "../lib/slug";
import { SlugInput } from "./SlugInput";

function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <SlugInput aria-label="name" value={value} onChange={setValue} />;
}

describe("slugify", () => {
  it.each([
    ["web", "web"],
    ["My App", "my-app"],
    ["my_app", "my-app"],
    ["web  -  api", "web-api"],
    [" web", "web"],
    ["web-", "web-"],
    ["we!b@#", "web"],
    ["café", "caf"],
  ])("%j → %j", (raw, want) => {
    expect(slugify(raw)).toBe(want);
  });
});

describe("SlugInput", () => {
  it("turns spaces into hyphens and ignores characters a slug can't hold", async () => {
    render(<Harness />);
    const input = screen.getByRole("textbox", { name: "name" });

    await userEvent.type(input, "My Web!App");

    expect(input).toHaveValue("my-webapp");
  });

  it("keeps the caret in place when a typed character is rejected", async () => {
    render(<Harness initial="webapp" />);
    const input = screen.getByRole<HTMLInputElement>("textbox", {
      name: "name",
    });

    await userEvent.type(input, "!-", {
      initialSelectionStart: 3,
      initialSelectionEnd: 3,
    });

    expect(input).toHaveValue("web-app");
    expect(input.selectionStart).toBe(4);
  });

  it("trims a trailing hyphen on blur", async () => {
    render(<Harness />);
    const input = screen.getByRole("textbox", { name: "name" });

    await userEvent.type(input, "web ");
    expect(input).toHaveValue("web-");
    await userEvent.tab();

    expect(input).toHaveValue("web");
  });
});
