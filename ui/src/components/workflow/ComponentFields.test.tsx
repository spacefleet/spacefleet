import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ComponentFields, type EditableComponent } from "./ComponentFields";

function makeComponent(
  overrides: Partial<EditableComponent> = {},
): EditableComponent {
  return {
    id: "node-1",
    name: "test-node",
    type: "terraform",
    config: {},
    continue_on_failure: false,
    requires_approval: true,
    target_cluster_id: null,
    target_namespace: "",
    chart_credential_id: null,
    github_installation_id: null,
    ...overrides,
  };
}

// ComponentFields is controlled, so the tests render it under a stateful
// harness that loops onChange back into the component prop — the same wiring
// NodeEditor gives it.
function Harness({
  initial,
  onComponent,
}: {
  initial: EditableComponent;
  onComponent: (c: EditableComponent) => void;
}) {
  const [component, setComponent] = useState(initial);
  return (
    <ComponentFields
      component={component}
      onChange={(next) => {
        setComponent(next);
        onComponent(next);
      }}
      clusters={[]}
      credentials={[]}
      cloudCredentials={[]}
      installations={[]}
      githubEnabled={false}
    />
  );
}

// Regression: these editors used to derive their rows from the serialized
// config value on every render, and the serializers drop blank rows — so
// "+ Add flag" / "+ Add values source" appended an empty row that vanished in
// the round-trip and the buttons did nothing.
describe("terraform extra-flags editors", () => {
  it("'+ Add flag' shows a new empty flag box even though blanks aren't stored", async () => {
    let last: EditableComponent | undefined;
    render(<Harness initial={makeComponent()} onComponent={(c) => (last = c)} />);

    // One editor each for init/plan/apply flags; the first is init.
    const addButtons = screen.getAllByRole("button", { name: "+ Add flag" });
    expect(addButtons).toHaveLength(3);
    await userEvent.click(addButtons[0]);

    const box = screen.getByRole("textbox", { name: "Flag 1" });
    expect(box).toHaveValue("");
    // The blank row lives only in the editor until it has content.
    expect(last?.config.init_flags ?? "").toBe("");

    await userEvent.type(box, "-upgrade");
    expect(last?.config.init_flags).toBe(JSON.stringify(["-upgrade"]));
  });

  it("keeps a flag row on screen while its text is cleared", async () => {
    let last: EditableComponent | undefined;
    render(
      <Harness
        initial={makeComponent({ config: { plan_flags: '["-var=env=prod"]' } })}
        onComponent={(c) => (last = c)}
      />,
    );

    const box = screen.getByRole("textbox", { name: "Flag 1" });
    await userEvent.clear(box);
    expect(screen.getByRole("textbox", { name: "Flag 1" })).toHaveValue("");
    expect(last?.config.plan_flags).toBe("");

    await userEvent.type(box, "-parallelism=20");
    expect(last?.config.plan_flags).toBe(JSON.stringify(["-parallelism=20"]));
  });
});

describe("helm values-from-git editor", () => {
  it("'+ Add values source' shows an editable row, stored once a repo URL is typed", async () => {
    let last: EditableComponent | undefined;
    render(
      <Harness
        initial={makeComponent({ type: "helm" })}
        onComponent={(c) => (last = c)}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "+ Add values source" }),
    );

    const repoBox = screen.getByRole("textbox", {
      name: "Source 1 repository URL",
    });
    expect(repoBox).toHaveValue("");
    expect(last?.config.values_sources ?? "").toBe("");

    await userEvent.type(repoBox, "https://github.com/acme/config.git");
    await userEvent.type(
      screen.getByRole("textbox", { name: "Source 1 values file path" }),
      "envs/prod/values.yaml",
    );
    expect(last?.config.values_sources).toBe(
      JSON.stringify([
        {
          repo_url: "https://github.com/acme/config.git",
          path: "envs/prod/values.yaml",
        },
      ]),
    );
  });
});
