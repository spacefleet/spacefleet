import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ComponentFields, type EditableComponent } from "./ComponentFields";
import type { components } from "../../api/schema";

type CloudCredential = components["schemas"]["CloudCredential"];

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
    approval_policy: null,
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
  upstreamOutputs,
  disabled,
  cloudCredentials = [],
}: {
  initial: EditableComponent;
  onComponent: (c: EditableComponent) => void;
  upstreamOutputs?: string[];
  disabled?: boolean;
  cloudCredentials?: CloudCredential[];
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
      cloudCredentials={cloudCredentials}
      installations={[]}
      githubEnabled={false}
      upstreamOutputs={upstreamOutputs}
      disabled={disabled}
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

describe("outputs reference helper", () => {
  it("inserts a components reference stub into the values and the namespace", async () => {
    let last: EditableComponent | undefined;
    render(
      <Harness
        initial={makeComponent({
          type: "helm",
          config: { values: "replicaCount: 2\n" },
          target_namespace: "apps-",
        })}
        onComponent={(c) => (last = c)}
        upstreamOutputs={["infra"]}
      />,
    );

    // One insert button under the values textarea, one under the namespace.
    const buttons = screen.getAllByRole("button", { name: "infra" });
    expect(buttons).toHaveLength(2);

    await userEvent.click(buttons[0]);
    expect(last?.config.values).toBe(
      "replicaCount: 2\n${{ components.infra.outputs. }}",
    );

    await userEvent.click(screen.getAllByRole("button", { name: "infra" })[1]);
    expect(last?.target_namespace).toBe("apps-${{ components.infra.outputs. }}");
  });

  it("renders nothing without upstream OpenTofu components or for a viewer", () => {
    const { rerender } = render(
      <Harness
        initial={makeComponent({ type: "helm" })}
        onComponent={() => {}}
      />,
    );
    expect(screen.queryByText(/Insert an output reference/)).toBeNull();

    rerender(
      <Harness
        initial={makeComponent({ type: "helm" })}
        onComponent={() => {}}
        upstreamOutputs={["infra"]}
        disabled
      />,
    );
    expect(screen.queryByText(/Insert an output reference/)).toBeNull();
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

describe("terraform inputs and workspace", () => {
  it("stores the TF_VAR opt-in as config.expose_tf_vars and the workspace as config.workspace", async () => {
    const user = userEvent.setup();
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent()}
        onComponent={(c) => {
          latest = c;
        }}
      />,
    );
    const toggle = screen.getByRole("checkbox", {
      name: "Expose variables as OpenTofu inputs",
    });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    expect(latest!.config.expose_tf_vars).toBe("true");
    await user.click(toggle);
    expect(latest!.config.expose_tf_vars).toBe("");

    await user.type(screen.getByPlaceholderText("default"), "prod");
    expect(latest!.config.workspace).toBe("prod");
  });

  it("stores the typed inputs as config.tfvars and flags JSON the server would reject", () => {
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent()}
        onComponent={(c) => {
          latest = c;
        }}
      />,
    );
    const field = screen.getByLabelText("Input variables");
    fireEvent.change(field, { target: { value: '{"replicas": 3' } });
    expect(latest!.config.tfvars).toBe('{"replicas": 3');
    expect(screen.getByText("Not valid JSON.")).toBeInTheDocument();

    fireEvent.change(field, { target: { value: '["a"]' } });
    expect(screen.getByText(/Must be a JSON object/)).toBeInTheDocument();

    fireEvent.change(field, { target: { value: '{"1st": "x"}' } });
    expect(screen.getByText('"1st" is not a valid variable name.')).toBeInTheDocument();

    fireEvent.change(field, {
      target: { value: '{"replicas": 3, "tags": {"team": "core"}}' },
    });
    expect(latest!.config.tfvars).toBe('{"replicas": 3, "tags": {"team": "core"}}');
    expect(screen.queryByText(/Not valid JSON|Must be a JSON object|not a valid variable name/)).not.toBeInTheDocument();
  });
});

describe("terraform state backends", () => {
  const creds: CloudCredential[] = [
    { id: "c-aws", name: "prod-aws", provider: "aws", config: {}, created_at: "", updated_at: "" },
    { id: "c-gcp", name: "prod-gcp", provider: "gcp", config: {}, created_at: "", updated_at: "" },
  ];

  it("switches backends, clearing the old settings and offering matching credentials", async () => {
    const user = userEvent.setup();
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent({
          config: {
            backend: "s3",
            backend_config: JSON.stringify({ bucket: "b", key: "k", region: "r" }),
            cloud_credential_id: "c-aws",
          },
        })}
        onComponent={(c) => {
          latest = c;
        }}
        cloudCredentials={creds}
      />,
    );
    // S3: only the aws credential is offered.
    const credSelect = screen.getByLabelText("Cloud credential");
    expect(credSelect).toHaveTextContent("prod-aws");
    expect(credSelect).not.toHaveTextContent("prod-gcp");

    await user.selectOptions(screen.getByLabelText("State backend"), "gcs");
    expect(latest!.config.backend).toBe("gcs");
    expect(latest!.config.backend_config).toBe("");
    expect(latest!.config.cloud_credential_id).toBe("");
    expect(screen.getByLabelText("Cloud credential")).toHaveTextContent("prod-gcp");
    expect(screen.queryByText("DynamoDB lock table")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("GCS bucket"), "acme-state");
    await user.type(screen.getByLabelText("GCS prefix"), "envs/prod");
    expect(JSON.parse(latest!.config.backend_config)).toEqual({ bucket: "acme-state", prefix: "envs/prod" });

    await user.selectOptions(screen.getByLabelText("State backend"), "azurerm");
    await user.type(screen.getByLabelText("Storage account"), "acmestate");
    await user.type(screen.getByLabelText("Container"), "tfstate");
    await user.type(screen.getByLabelText("Azure state key"), "prod.tfstate");
    expect(JSON.parse(latest!.config.backend_config)).toEqual({
      storage_account_name: "acmestate",
      container_name: "tfstate",
      key: "prod.tfstate",
    });
  });
});

describe("approval policy", () => {
  it("edits the gate's policy and stores the default as null", async () => {
    const user = userEvent.setup();
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent({ type: "helm", requires_approval: true })}
        onComponent={(c) => {
          latest = c;
        }}
      />,
    );
    await user.type(screen.getByLabelText("Approvers"), "Ops@example.com, sre@example.com");
    expect(latest!.approval_policy?.approvers).toEqual(["Ops@example.com", "sre@example.com"]);
    fireEvent.change(screen.getByLabelText("Approvals required"), { target: { value: "2" } });
    expect(latest!.approval_policy?.required).toBe(2);
    await user.click(screen.getByRole("checkbox", { name: /different approver/ }));
    expect(latest!.approval_policy?.require_different_approver).toBe(true);
    fireEvent.change(screen.getByLabelText("Approval timeout (minutes)"), { target: { value: "90" } });
    expect(latest!.approval_policy?.timeout_minutes).toBe(90);

    // Clearing everything returns to the default policy.
    await user.clear(screen.getByLabelText("Approvers"));
    fireEvent.change(screen.getByLabelText("Approvals required"), { target: { value: "1" } });
    await user.click(screen.getByRole("checkbox", { name: /different approver/ }));
    fireEvent.change(screen.getByLabelText("Approval timeout (minutes)"), { target: { value: "" } });
    expect(latest!.approval_policy).toBeNull();
  });
});

describe("terraform backend extras", () => {
  it("stores the GCS KMS key and the Azure AD auth flag in backend_config", async () => {
    const user = userEvent.setup();
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent({
          config: { backend: "gcs", backend_config: JSON.stringify({ bucket: "b", prefix: "p" }) },
        })}
        onComponent={(c) => {
          latest = c;
        }}
      />,
    );
    await user.type(screen.getByLabelText("GCS KMS key"), "projects/p/k");
    expect(JSON.parse(latest!.config.backend_config)).toEqual({ bucket: "b", prefix: "p", kms_encryption_key: "projects/p/k" });

    await user.selectOptions(screen.getByLabelText("State backend"), "azurerm");
    const aad = screen.getByLabelText("Use Azure AD authentication");
    expect(aad).not.toBeChecked();
    await user.click(aad);
    expect(JSON.parse(latest!.config.backend_config)).toEqual({ use_azuread_auth: "true" });
    await user.click(aad);
    expect(latest!.config.backend_config).toBe("");
  });
});

describe("managed state backend", () => {
  const creds: CloudCredential[] = [
    { id: "c-aws", name: "prod-aws", provider: "aws", config: {}, created_at: "", updated_at: "" },
    { id: "c-gcp", name: "prod-gcp", provider: "gcp", config: {}, created_at: "", updated_at: "" },
  ];
  const original = window.appConfig;
  afterEach(() => {
    window.appConfig = original;
  });

  it("is offered first when the server can keep state, takes no settings, and lets any cloud's credential serve the providers", async () => {
    window.appConfig = { ...original, managedStateEnabled: true };
    const user = userEvent.setup();
    let latest: EditableComponent | null = null;
    render(
      <Harness
        initial={makeComponent({
          config: {
            backend: "s3",
            backend_config: JSON.stringify({ bucket: "b", key: "k", region: "r" }),
            cloud_credential_id: "c-aws",
          },
        })}
        onComponent={(c) => {
          latest = c;
        }}
        cloudCredentials={creds}
      />,
    );
    const select = screen.getByLabelText("State backend") as HTMLSelectElement;
    expect(select.options[0].value).toBe("spacefleet");
    expect(select.options[0].textContent).toBe("Spacefleet (managed)");
    expect(screen.getByText("The S3 bucket holding the state.")).toBeInTheDocument();

    await user.selectOptions(select, "spacefleet");
    expect(latest!.config.backend).toBe("spacefleet");
    expect(latest!.config.backend_config).toBe("");
    expect(latest!.config.cloud_credential_id).toBe("");
    expect(screen.queryByText("The S3 bucket holding the state.")).not.toBeInTheDocument();
    expect(screen.getByText(/Spacefleet keeps this component's state/)).toBeInTheDocument();
    const credSelect = screen.getByLabelText("Cloud credential");
    expect(credSelect).toHaveTextContent("prod-aws (AWS)");
    expect(credSelect).toHaveTextContent("prod-gcp (Google Cloud)");
  });

  it("is not offered when the server can't keep state, unless the component already uses it", () => {
    window.appConfig = { ...original, managedStateEnabled: false };
    const { unmount } = render(
      <Harness initial={makeComponent({ config: { backend: "s3" } })} onComponent={() => {}} />,
    );
    const values = Array.from((screen.getByLabelText("State backend") as HTMLSelectElement).options).map((o) => o.value);
    expect(values).not.toContain("spacefleet");
    unmount();

    render(<Harness initial={makeComponent({ config: { backend: "spacefleet" } })} onComponent={() => {}} />);
    expect((screen.getByLabelText("State backend") as HTMLSelectElement).value).toBe("spacefleet");
  });
});
