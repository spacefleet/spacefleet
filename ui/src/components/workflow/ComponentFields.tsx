import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import type { components } from "../../api/schema";
import { CHART_SOURCES } from "../chartSources";
import { parseBackendConfig } from "./backendConfig";
import {
  TOFU_DEFAULT_VERSION,
  TOFU_VERSIONS,
  tofuNativeLock,
} from "../../lib/tofuVersions";
import { managedStateEnabled } from "../../lib/appConfig";
import { SlugInput } from "../SlugInput";
import { RepositoryPicker } from "./RepositoryPicker";
import { outputsRefSnippet } from "./outputsRefs";
import { RefAutocompleteField } from "./RefAutocompleteField";
import type { RefContext } from "./refAutocomplete";
import {
  parseValuesSources,
  serializeValuesSources,
  type ValuesSourceRow,
} from "./valuesSources";

type ComponentType = components["schemas"]["ComponentType"];
type ChartSource = components["schemas"]["ChartSource"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type CloudCredential = components["schemas"]["CloudCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];
type GitHubRepository = components["schemas"]["GitHubRepository"];

// EditableComponent is the builder's working copy of one component — the
// fields the editor edits. The stage it runs in is the draft stage holding it.
export interface EditableComponent {
  id: string;
  name: string;
  type: ComponentType;
  config: Record<string, string>;
  continue_on_failure: boolean;
  // When true the run parks for a human before this step runs. An OpenTofu node
  // defaults this on (its apply waits for review after the plan); helm/manifest
  // nodes can opt in too.
  requires_approval: boolean;
  // The policy applied at that gate; null is the default (any editor, one
  // approval, self-approval allowed, no timeout).
  approval_policy: ApprovalPolicy | null;
  target_cluster_id: string | null;
  target_namespace: string;
  chart_credential_id: string | null;
  github_installation_id: string | null;
}

type ApprovalPolicy = components["schemas"]["ApprovalPolicy"];

interface ComponentFieldsProps {
  component: EditableComponent;
  onChange: (next: EditableComponent) => void;
  clusters: Cluster[];
  credentials: ChartCredential[];
  cloudCredentials: CloudCredential[];
  installations: GitHubInstallation[];
  githubEnabled: boolean;
  // When true the fields render read-only. Inputs are disabled rather than
  // hidden so the configuration stays inspectable.
  disabled?: boolean;
  // Names of the upstream OpenTofu components whose outputs this node may
  // reference (its transitive dependencies — the editor computes them from the
  // draft graph). Drives the insert-a-reference helper on the helm values and
  // namespace fields.
  upstreamOutputs?: string[];
  // The references the ${{ }} autocomplete may complete on the helm interpolable
  // fields (variable names, upstream component names, known output keys).
  refContext?: RefContext;
}

// EMPTY_REF_CONTEXT is the default when a caller doesn't supply one (e.g. a
// read-only embed) — the autocomplete then simply offers the namespaces.
const EMPTY_REF_CONTEXT: RefContext = {
  varsNames: [],
  componentNames: [],
  outputKeysByName: {},
};

// ComponentFields renders the editable form for one workflow node, independent
// of any container (it was extracted from the old side panel so the full-page
// NodeEditor can reuse it). Type-specific config (helm: chart source +
// repo/chart/version/git + values + a values-sources editor; manifest:
// repo/ref/path), continue-on-failure, target overrides, and
// credential/installation pickers. Client-side validation is intentionally light
// — the server (PUT /workflow) is the source of truth.
export function ComponentFields({
  component,
  onChange,
  clusters,
  credentials,
  cloudCredentials,
  installations,
  githubEnabled,
  disabled = false,
  upstreamOutputs = [],
  refContext = EMPTY_REF_CONTEXT,
}: ComponentFieldsProps) {
  function set<K extends keyof EditableComponent>(
    key: K,
    value: EditableComponent[K],
  ) {
    onChange({ ...component, [key]: value });
  }
  function setConfig(key: string, value: string) {
    onChange({ ...component, config: { ...component.config, [key]: value } });
  }
  // setConfigs writes several keys in one change, for edits that must land
  // together (each setConfig call spreads the *current* component, so two
  // calls in a row would drop the first).
  function setConfigs(entries: Record<string, string>) {
    onChange({ ...component, config: { ...component.config, ...entries } });
  }

  const chartSource =
    (component.config.chart_source as ChartSource) || "http_repo";

  // A repository can be picked when the GitHub App is configured and the org has
  // at least one installation; otherwise the user types the URL by hand.
  const canPickRepo = githubEnabled && installations.length > 0;

  // pickRepoInto sets a config repo-URL field to the picked clone URL and, in the
  // same update, the installation it was listed under. The server resolves the
  // installation from the repository URL on save anyway (so a typed URL works
  // too); sending the picked one keeps it when an account is connected twice.
  function pickRepoInto(repoUrlKey: string) {
    return (repo: GitHubRepository) =>
      onChange({
        ...component,
        config: { ...component.config, [repoUrlKey]: repo.clone_url },
        github_installation_id: repo.installation_id,
      });
  }

  // pickValuesSourceRepo is the same idea for a git values source row: the
  // editor hands us its rows already serialized (with the picked repo URL
  // applied) so the config key and the component-level installation land in
  // one onChange and neither change clobbers the other.
  function pickValuesSourceRepo(serialized: string, repo: GitHubRepository) {
    onChange({
      ...component,
      config: { ...component.config, values_sources: serialized },
      github_installation_id: repo.installation_id,
    });
  }

  // The picker button for a "repo_url" config field — shared by the helm git
  // source, manifest, and terraform forms (all use the repo_url key). Null when
  // the picker isn't available so the forms render just the text input.
  const repoUrlPicker: ReactNode = canPickRepo ? (
    <RepositoryPicker disabled={disabled} onSelect={pickRepoInto("repo_url")} />
  ) : null;

  return (
    <div className="space-y-4">
      <Section title="General">
        <Field label="Name" help={nameHelp(component.type)}>
          <SlugInput
            className="w-full border border-neutral-700 px-3 py-2 text-sm"
            value={component.name}
            onChange={(name) => set("name", name)}
            placeholder="web"
            disabled={disabled}
          />
        </Field>
        <label className="flex items-center gap-2 text-sm text-neutral-300">
          <input
            type="checkbox"
            className="h-4 w-4 accent-white"
            checked={component.continue_on_failure}
            onChange={(e) => set("continue_on_failure", e.target.checked)}
            disabled={disabled}
          />
          Continue on failure
        </label>
      </Section>

      {component.type === "helm" ? (
        <HelmConfig
          config={component.config}
          chartSource={chartSource}
          setConfig={setConfig}
          repoUrlPicker={repoUrlPicker}
          canPickRepo={canPickRepo}
          onPickValuesSourceRepo={pickValuesSourceRepo}
          disabled={disabled}
          upstreamOutputs={upstreamOutputs}
          refContext={refContext}
        />
      ) : component.type === "terraform" ? (
        <TerraformConfig
          config={component.config}
          setConfig={setConfig}
          setConfigs={setConfigs}
          repoUrlPicker={repoUrlPicker}
          cloudCredentials={cloudCredentials}
          clusters={clusters}
          disabled={disabled}
        />
      ) : (
        <ManifestConfig
          config={component.config}
          setConfig={setConfig}
          repoUrlPicker={repoUrlPicker}
          disabled={disabled}
        />
      )}

      {/* Credentials. Helm http_repo/oci take a chart credential; a private git
          source needs none here — the server attaches the GitHub installation
          on the repository's account when the workflow is saved. */}
      {component.type === "helm" &&
        (chartSource === "http_repo" || chartSource === "oci") && (
          <Field
            label="Chart credential"
            help="Optional — only for a private repo/registry."
          >
            <select
              className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
              value={component.chart_credential_id ?? ""}
              onChange={(e) =>
                set("chart_credential_id", e.target.value || null)
              }
              disabled={disabled}
            >
              <option value="">None (public chart)</option>
              {credentials.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
        )}

      {/* Targeting. Helm + manifest deploy to a cluster (required); terraform
          manages cloud infra and has no cluster target, so the fields are hidden
          for it. The namespace is only meaningful for helm — a manifest carries
          its own namespaces. */}
      {component.type !== "terraform" && (
        <Field
          label="Target cluster"
          help="The cluster this component deploys into."
        >
          <select
            className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
            value={component.target_cluster_id ?? ""}
            onChange={(e) => set("target_cluster_id", e.target.value || null)}
            disabled={disabled}
          >
            <option value="">Select a cluster…</option>
            {clusters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
      )}

      {component.type === "helm" && (
        <Field
          label="Target namespace"
          help="The namespace this release deploys into. Supports ${{ }} references, e.g. an upstream output like ${{ components.infra.outputs.namespace }}."
        >
          <RefAutocompleteField
            as="input"
            className="w-full border border-neutral-700 px-3 py-2 text-sm"
            value={component.target_namespace}
            onChange={(v) => set("target_namespace", v)}
            context={refContext}
            disabled={disabled}
          />
          <OutputRefButtons
            names={upstreamOutputs}
            disabled={disabled}
            onInsert={(snippet) =>
              set("target_namespace", component.target_namespace + snippet)
            }
          />
        </Field>
      )}

      <Section title="Approval">
        {/* Approval gate. An OpenTofu node frames it as an Auto-approve opt-out
            (gated by default — the run plans, then pauses for a human to review
            before applying); every other node frames it as an opt-in manual-
            approval requirement. Both edit the same requires_approval flag. */}
        {component.type === "terraform" ? (
          <Field help="Off (the default): the run plans, then pauses for a human to review the plan and approve before applying.">
            <label className="flex items-center gap-2 text-sm text-neutral-300">
              <input
                type="checkbox"
                className="h-4 w-4 accent-white"
                checked={!component.requires_approval}
                onChange={(e) => set("requires_approval", !e.target.checked)}
                disabled={disabled}
              />
              Auto-approve apply
            </label>
          </Field>
        ) : (
          <Field help="When on, the run pauses at this step until a human approves it.">
            <label className="flex items-center gap-2 text-sm text-neutral-300">
              <input
                type="checkbox"
                className="h-4 w-4 accent-white"
                checked={component.requires_approval}
                onChange={(e) => set("requires_approval", e.target.checked)}
                disabled={disabled}
              />
              Require manual approval
            </label>
          </Field>
        )}
        {component.requires_approval && (
          <ApprovalPolicyFields
            policy={component.approval_policy}
            onChange={(p) => set("approval_policy", p)}
            disabled={disabled}
          />
        )}
      </Section>
    </div>
  );
}

// ApprovalPolicyFields edits the gate's policy: named approvers, how many
// approvals open the gate, whether the run's starter may approve, and a
// timeout. Every field empty/off is the default policy (stored as null).
function ApprovalPolicyFields({
  policy,
  onChange,
  disabled,
}: {
  policy: ApprovalPolicy | null;
  onChange: (next: ApprovalPolicy | null) => void;
  disabled: boolean;
}) {
  const p = policy ?? {};
  const approverCount = (p.approvers ?? []).length;
  // The approvers box is free text while typing (a trailing comma must
  // survive a keystroke), parsed into the list on every change.
  const [approversText, setApproversText] = useState(
    (p.approvers ?? []).join(", "),
  );
  function update(patch: Partial<ApprovalPolicy>) {
    const next: ApprovalPolicy = { ...p, ...patch };
    if (!next.approvers?.length) delete next.approvers;
    if (!next.required || next.required <= 1) delete next.required;
    if (!next.require_different_approver)
      delete next.require_different_approver;
    if (!next.timeout_minutes) delete next.timeout_minutes;
    onChange(Object.keys(next).length === 0 ? null : next);
  }
  return (
    <div className="ml-6 space-y-3 border-l-2 border-neutral-800 pl-4">
      <Field
        label="Approvers"
        help="Who may approve, by email, comma-separated. Empty lets any editor or admin approve. Anyone with edit access can still reject."
      >
        <input
          type="text"
          aria-label="Approvers"
          className="w-full border border-neutral-700 px-3 py-2 text-sm"
          placeholder="ops@example.com, sre@example.com"
          value={approversText}
          onChange={(e) => {
            setApproversText(e.target.value);
            update({
              approvers: e.target.value
                .split(",")
                .map((a) => a.trim())
                .filter((a) => a !== ""),
            });
          }}
          disabled={disabled}
        />
      </Field>
      <Field
        label="Approvals required"
        help={
          approverCount > 0
            ? `How many of the ${approverCount} named approvers must approve (N-of-M).`
            : "How many distinct people must approve before the step runs."
        }
      >
        <input
          type="number"
          aria-label="Approvals required"
          min={1}
          max={approverCount > 0 ? approverCount : undefined}
          className="w-24 border border-neutral-700 px-3 py-2 text-sm"
          value={p.required ?? 1}
          onChange={(e) =>
            update({ required: Math.max(1, Number(e.target.value) || 1) })
          }
          disabled={disabled}
        />
      </Field>
      <label className="flex items-center gap-2 text-sm text-neutral-300">
        <input
          type="checkbox"
          className="h-4 w-4 accent-white"
          checked={p.require_different_approver ?? false}
          onChange={(e) =>
            update({ require_different_approver: e.target.checked })
          }
          disabled={disabled}
        />
        Require a different approver than whoever started the run
      </label>
      <Field
        label="Approval timeout (minutes)"
        help="Fail the run if nobody decides within this time. Empty or 0 waits indefinitely."
      >
        <input
          type="number"
          aria-label="Approval timeout (minutes)"
          min={0}
          max={10080}
          className="w-28 border border-neutral-700 px-3 py-2 text-sm"
          value={p.timeout_minutes ?? ""}
          onChange={(e) =>
            update({
              timeout_minutes: Math.max(0, Number(e.target.value) || 0),
            })
          }
          disabled={disabled}
        />
      </Field>
    </div>
  );
}

function HelmConfig({
  config,
  chartSource,
  setConfig,
  repoUrlPicker,
  canPickRepo,
  onPickValuesSourceRepo,
  disabled,
  upstreamOutputs,
  refContext,
}: {
  config: Record<string, string>;
  chartSource: ChartSource;
  setConfig: (key: string, value: string) => void;
  repoUrlPicker: ReactNode;
  canPickRepo: boolean;
  onPickValuesSourceRepo: (serialized: string, repo: GitHubRepository) => void;
  disabled: boolean;
  upstreamOutputs: string[];
  refContext: RefContext;
}) {
  const source =
    CHART_SOURCES.find((s) => s.value === chartSource) ?? CHART_SOURCES[0];
  return (
    <>
      <Field label="Chart source">
        <select
          className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
          value={chartSource}
          onChange={(e) => setConfig("chart_source", e.target.value)}
          disabled={disabled}
        >
          {CHART_SOURCES.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </select>
      </Field>

      {source.fields.map((field) => (
        <Field key={field.key} label={field.label} help={field.help}>
          {/* The git source's repository URL gets the picker; other sources
              (http_repo/oci) point at registries the picker doesn't enumerate. */}
          <WithPicker
            picker={
              field.key === "repo_url" && chartSource === "git"
                ? repoUrlPicker
                : null
            }
          >
            <input
              type="text"
              className="w-full border border-neutral-700 px-3 py-2 text-sm"
              placeholder={field.placeholder}
              value={config[field.key] ?? ""}
              onChange={(e) => setConfig(field.key, e.target.value)}
              disabled={disabled}
            />
          </WithPicker>
        </Field>
      ))}

      <Field
        label="Release name"
        help="Defaults to <application>-<component>. Set to override."
      >
        <RefAutocompleteField
          as="input"
          className="w-full border border-neutral-700 px-3 py-2 text-sm"
          placeholder="(defaults to <app>-<component>)"
          value={config.release_name ?? ""}
          onChange={(v) => setConfig("release_name", v)}
          context={refContext}
          disabled={disabled}
        />
      </Field>

      <Field
        label="Values (values.yaml)"
        help={
          "Optional inline overrides. Supports ${{ vars.NAME }}, run context like ${{ run.git_sha_short }}, and upstream OpenTofu outputs like ${{ components.infra.outputs.namespace }} — substituted when a run starts."
        }
      >
        <RefAutocompleteField
          as="textarea"
          className="h-32 w-full border border-neutral-700 px-3 py-2 font-mono text-xs"
          placeholder={"replicaCount: 2\n"}
          value={config.values ?? ""}
          onChange={(v) => setConfig("values", v)}
          context={refContext}
          disabled={disabled}
        />
        <OutputRefButtons
          names={upstreamOutputs}
          disabled={disabled}
          onInsert={(snippet) =>
            setConfig("values", (config.values ?? "") + snippet)
          }
        />
      </Field>

      <ValuesSourcesEditor
        value={config.values_sources}
        onChange={(v) => setConfig("values_sources", v)}
        canPickRepo={canPickRepo}
        onPickRepo={onPickValuesSourceRepo}
        disabled={disabled}
      />
    </>
  );
}

function ManifestConfig({
  config,
  setConfig,
  repoUrlPicker,
  disabled,
}: {
  config: Record<string, string>;
  setConfig: (key: string, value: string) => void;
  repoUrlPicker: ReactNode;
  disabled: boolean;
}) {
  return (
    <>
      <Field label="Repository URL">
        <WithPicker picker={repoUrlPicker}>
          <input
            type="text"
            className="w-full border border-neutral-700 px-3 py-2 text-sm"
            placeholder="https://github.com/org/manifests.git"
            value={config.repo_url ?? ""}
            onChange={(e) => setConfig("repo_url", e.target.value)}
            disabled={disabled}
          />
        </WithPicker>
      </Field>
      <Field label="Branch or tag">
        <input
          type="text"
          className="w-full border border-neutral-700 px-3 py-2 text-sm"
          placeholder="(default branch)"
          value={config.git_ref ?? ""}
          onChange={(e) => setConfig("git_ref", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Path" help="File or directory to kubectl apply.">
        <input
          type="text"
          className="w-full border border-neutral-700 px-3 py-2 text-sm"
          placeholder="manifests/prod"
          value={config.path ?? ""}
          onChange={(e) => setConfig("path", e.target.value)}
          disabled={disabled}
        />
      </Field>
    </>
  );
}

// TerraformConfig edits an OpenTofu node: the git source (repo_url/ref/path),
// the managed state backend, and the cloud credential the run signs in with.
// There is no command field — an OpenTofu node is a single step that runs plan
// then (once approved) apply; the run synthesizes those two commands, so the
// user configures the source once here.
//
// The state backend is always managed: Spacefleet writes a backend override
// into the module at init, so state lands where this config says regardless of
// any backend block in code (pointing the fields at existing state adopts it
// in place). "spacefleet" keeps the state in Spacefleet itself and has no
// settings; the cloud backends (s3, gcs, azurerm) have dedicated fields that
// serialize into the config.backend_config JSON object the server validates
// and renders. backend_config gets the same redaction treatment as helm inline
// values (see lib/api workflow secret keys), so non-editors see blank fields.
function TerraformConfig({
  config,
  setConfig,
  setConfigs,
  repoUrlPicker,
  cloudCredentials,
  clusters,
  disabled,
}: {
  config: Record<string, string>;
  setConfig: (key: string, value: string) => void;
  setConfigs: (entries: Record<string, string>) => void;
  repoUrlPicker: ReactNode;
  cloudCredentials: CloudCredential[];
  clusters: Cluster[];
  disabled: boolean;
}) {
  // The backend and its settings. backend_config is one flat JSON object
  // whose keys depend on the backend (s3: bucket/key/region/…; gcs:
  // bucket/prefix; azurerm: storage_account_name/container_name/key/…).
  const backend = config.backend || "s3";
  const s3 = parseBackendConfig(config.backend_config);
  const encrypt = s3.encrypt === "true";

  // The OpenTofu line this component runs (absent = the server's default
  // line) decides the locking story below: 1.10+ locks state natively in the
  // bucket — automatic, nothing to set up — while 1.9 locks via a DynamoDB
  // table.
  const tofuVersion = config.tofu_version || TOFU_DEFAULT_VERSION;
  const nativeLock = tofuNativeLock(config.tofu_version);

  // With a cloud backend the credential must match the backend's cloud: the
  // same credential signs the run in to the state backend and to the module's
  // providers. Managed state needs no credential, so any cloud's may serve
  // the module's providers.
  const managed = backend === "spacefleet";
  const backendProvider = BACKEND_PROVIDER[backend] ?? "aws";
  const matchingCredentials = managed
    ? cloudCredentials
    : cloudCredentials.filter((c) => c.provider === backendProvider);
  // The managed option is offered when the server can keep state — or when
  // this component already uses it, so the select never misreports.
  const offerManaged = managed || managedStateEnabled();
  // What the collapsed Credentials section shows, so a credential that's set
  // is never hidden behind it.
  const credentialsSummary = [
    cloudCredentials.find((c) => c.id === config.cloud_credential_id)?.name,
    clusters.find((c) => c.id === config.auth_cluster_id)?.name,
  ]
    .filter(Boolean)
    .join(" · ");
  // And for the collapsed Advanced section: the workspace and how many extra
  // flags are set.
  const flagCount = [config.init_flags, config.plan_flags, config.apply_flags]
    .flatMap(parseFlags)
    .filter((f) => f.trim() !== "").length;
  const advancedSummary = [
    !managed && config.workspace ? `workspace ${config.workspace}` : "",
    flagCount === 0 ? "" : flagCount === 1 ? "1 flag" : `${flagCount} flags`,
  ]
    .filter(Boolean)
    .join(" · ");
  // Switching backends clears the settings (their keys differ) and the
  // credential (a different cloud) — in one change, so none is lost. Managed
  // state takes no workspace, so moving to it clears that too.
  function setBackend(next: string) {
    setConfigs({
      backend: next,
      backend_config: "",
      cloud_credential_id: "",
      ...(next === "spacefleet" ? { workspace: "" } : {}),
    });
  }

  // setS3 updates one backend setting, dropping emptied keys so the stored
  // JSON holds only what's set ("" omits backend_config entirely on save).
  function writeS3(next: Record<string, string>) {
    setConfig(
      "backend_config",
      Object.keys(next).length === 0 ? "" : JSON.stringify(next),
    );
  }
  function setS3(key: string, value: string) {
    const next = { ...s3 };
    if (value === "") delete next[key];
    else next[key] = value;
    writeS3(next);
  }
  function setEncrypt(on: boolean) {
    const next = { ...s3 };
    if (on) {
      next.encrypt = "true";
    } else {
      delete next.encrypt;
      // A KMS key is only meaningful with encryption on.
      delete next.kms_key_id;
    }
    writeS3(next);
  }

  return (
    <>
      <Section title="Repository">
        <Field label="Repository URL">
          <WithPicker picker={repoUrlPicker}>
            <input
              type="text"
              className="w-full border border-neutral-700 px-3 py-2 text-sm"
              placeholder="https://github.com/org/infra.git"
              value={config.repo_url ?? ""}
              onChange={(e) => setConfig("repo_url", e.target.value)}
              disabled={disabled}
            />
          </WithPicker>
        </Field>
        <Field label="Branch or tag">
          <input
            type="text"
            className="w-full border border-neutral-700 px-3 py-2 text-sm"
            placeholder="(default branch)"
            value={config.git_ref ?? ""}
            onChange={(e) => setConfig("git_ref", e.target.value)}
            disabled={disabled}
          />
        </Field>
        <Field label="Working path">
          <input
            type="text"
            className="w-full border border-neutral-700 px-3 py-2 text-sm"
            placeholder="(repository root)"
            value={config.path ?? ""}
            onChange={(e) => setConfig("path", e.target.value)}
            disabled={disabled}
          />
        </Field>
      </Section>
      <Section title="OpenTofu">
        <Field label="OpenTofu version">
          <select
            className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
            value={tofuVersion}
            onChange={(e) => setConfig("tofu_version", e.target.value)}
            disabled={disabled}
          >
            {/* A stored value outside the supported list (shouldn't happen —
                the server validates) still renders, so the select never lies
                about what's saved. */}
            {!TOFU_VERSIONS.some((v) => v.minor === tofuVersion) && (
              <option value={tofuVersion}>{tofuVersion} (unsupported)</option>
            )}
            {TOFU_VERSIONS.map((v, i) => (
              <option key={v.minor} value={v.minor}>
                {v.minor}
                {i === 0 ? " (latest)" : ""}
              </option>
            ))}
          </select>
        </Field>

        <Field
          label="State backend"
          help="Where this component's OpenTofu state lives. Spacefleet configures your module to use it at init (overriding any backend block in code) — to adopt existing state in your cloud, point it at the bucket and key your state is already in."
        >
          <select
            aria-label="State backend"
            className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
            value={backend}
            onChange={(e) => setBackend(e.target.value)}
            disabled={disabled}
          >
            {offerManaged && (
              <option value="spacefleet">Spacefleet (managed)</option>
            )}
            <optgroup label="Your cloud">
              <option value="s3">Amazon S3</option>
              <option value="gcs">Google Cloud Storage</option>
              <option value="azurerm">Azure Blob Storage</option>
            </optgroup>
          </select>
        </Field>

        {backend === "gcs" && (
          <>
            <Field
              label="Bucket"
              help="The Cloud Storage bucket holding the state."
            >
              <input
                type="text"
                aria-label="GCS bucket"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="acme-terraform-state"
                value={s3.bucket ?? ""}
                onChange={(e) => setS3("bucket", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="Prefix"
              help="Path prefix of the state within the bucket — must be unique per component. Locking is automatic (the bucket itself locks the state)."
            >
              <input
                type="text"
                aria-label="GCS prefix"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="envs/prod"
                value={s3.prefix ?? ""}
                onChange={(e) => setS3("prefix", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="KMS key"
              help="Optional — the Cloud KMS key the state objects are encrypted with (the credential needs Encrypter/Decrypter on it). Leave empty for Google-managed encryption. To use your own raw key instead, add a sensitive variable named GOOGLE_ENCRYPTION_KEY to this component — it must not be stored here."
            >
              <input
                type="text"
                aria-label="GCS KMS key"
                className="w-full border border-neutral-700 px-3 py-2 font-mono text-sm"
                placeholder="projects/p/locations/l/keyRings/r/cryptoKeys/k"
                value={s3.kms_encryption_key ?? ""}
                onChange={(e) => setS3("kms_encryption_key", e.target.value)}
                disabled={disabled}
              />
            </Field>
          </>
        )}

        {backend === "azurerm" && (
          <>
            <Field
              label="Storage account"
              help="The storage account holding the state."
            >
              <input
                type="text"
                aria-label="Storage account"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="acmetfstate"
                value={s3.storage_account_name ?? ""}
                onChange={(e) => setS3("storage_account_name", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="Container"
              help="The blob container within the storage account."
            >
              <input
                type="text"
                aria-label="Container"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="tfstate"
                value={s3.container_name ?? ""}
                onChange={(e) => setS3("container_name", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="State key"
              help="Blob name of the state file in the container — must be unique per component. Locking is automatic (a blob lease)."
            >
              <input
                type="text"
                aria-label="Azure state key"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="prod.tfstate"
                value={s3.key ?? ""}
                onChange={(e) => setS3("key", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="Resource group"
              help="Optional — the resource group of the storage account, when the credential needs it to look the account up."
            >
              <input
                type="text"
                aria-label="Resource group"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="(optional)"
                value={s3.resource_group_name ?? ""}
                onChange={(e) => setS3("resource_group_name", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field help="Authenticate to the storage account with the credential's Azure AD identity (a data-plane role such as Storage Blob Data Contributor) instead of the account's access keys. Use it when key access is disabled on the account.">
              <label className="flex items-center gap-2 text-sm text-neutral-300">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-white"
                  aria-label="Use Azure AD authentication"
                  checked={s3.use_azuread_auth === "true"}
                  onChange={(e) => setS3("use_azuread_auth", e.target.checked ? "true" : "")}
                  disabled={disabled}
                />
                Use Azure AD authentication for the storage account
              </label>
            </Field>
          </>
        )}

        {backend === "s3" && (
          <>
            <Field label="Bucket" help="The S3 bucket holding the state.">
              <input
                type="text"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="my-terraform-state"
                value={s3.bucket ?? ""}
                onChange={(e) => setS3("bucket", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field
              label="State key"
              help="Object path of the state file in the bucket — must be unique per component."
            >
              <input
                type="text"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="envs/prod/terraform.tfstate"
                value={s3.key ?? ""}
                onChange={(e) => setS3("key", e.target.value)}
                disabled={disabled}
              />
            </Field>
            <Field label="Region" help="AWS region of the bucket.">
              <input
                type="text"
                className="w-full border border-neutral-700 px-3 py-2 text-sm"
                placeholder="us-east-1"
                value={s3.region ?? ""}
                onChange={(e) => setS3("region", e.target.value)}
                disabled={disabled}
              />
            </Field>
            {nativeLock ? (
              <>
                <div className="border border-neutral-800 bg-neutral-800/50 px-3 py-2 text-sm text-neutral-300">
                  <span className="font-medium text-neutral-300">
                    State locking is automatic.
                  </span>{" "}
                  OpenTofu {tofuVersion} locks state in the bucket itself during
                  every plan and apply, so concurrent runs can't corrupt it —
                  there's nothing to set up.
                </div>
                <Field
                  label="DynamoDB lock table"
                  help="Optional. Only needed while this state is also used outside Spacefleet with DynamoDB locking — both locks are held, so you can migrate off the table safely."
                >
                  <input
                    type="text"
                    className="w-full border border-neutral-700 px-3 py-2 text-sm"
                    placeholder="(not needed)"
                    value={s3.dynamodb_table ?? ""}
                    onChange={(e) => setS3("dynamodb_table", e.target.value)}
                    disabled={disabled}
                  />
                </Field>
              </>
            ) : (
              <Field
                label="DynamoDB lock table"
                help="Recommended. Locks state during plan and apply so concurrent runs can't corrupt it — Spacefleet creates the table if it doesn't exist (with a cloud credential attached; instance-role runs need an existing table). Or pick OpenTofu 1.10+ above for automatic locking with no table at all."
              >
                <input
                  type="text"
                  className="w-full border border-neutral-700 px-3 py-2 text-sm"
                  placeholder="(no locking)"
                  value={s3.dynamodb_table ?? ""}
                  onChange={(e) => setS3("dynamodb_table", e.target.value)}
                  disabled={disabled}
                />
              </Field>
            )}

            <label className="flex items-center gap-2 text-sm text-neutral-300">
              <input
                type="checkbox"
                className="h-4 w-4 accent-white"
                checked={encrypt}
                onChange={(e) => setEncrypt(e.target.checked)}
                disabled={disabled}
              />
              Encrypt state at rest
            </label>
            {encrypt && (
              <Field
                label="KMS key"
                help="Optional. KMS key ARN or ID for SSE-KMS; empty uses SSE-S3 (AES-256)."
              >
                <input
                  type="text"
                  className="w-full border border-neutral-700 px-3 py-2 text-sm"
                  placeholder="(SSE-S3)"
                  value={s3.kms_key_id ?? ""}
                  onChange={(e) => setS3("kms_key_id", e.target.value)}
                  disabled={disabled}
                />
              </Field>
            )}
          </>
        )}

        {/* Typed inputs: a JSON object of variable name → value, written into
            the module as an auto-loaded tfvars file before every command.
            Stored verbatim as config.tfvars; the server validates the shape,
            the editor just flags unparsable JSON as you type. */}
        <TFVarsEditor
          value={config.tfvars}
          onChange={(v) => setConfig("tfvars", v)}
          disabled={disabled}
        />
      </Section>
      <Section
        title="Credentials"
        collapsible
        summary={credentialsSummary}
      >
        <Field
          label="Cloud credential"
          help={
            managed
              ? "Optional — a cloud credential for your module's providers. Managed state needs none. Leave empty to use the runner's own identity (an instance role or workload identity)."
              : `The ${PROVIDER_NAMES[backendProvider]} credential the run signs in with — used for the state backend and your module's ${PROVIDER_NAMES[backendProvider]} providers. Leave empty to use the runner's own identity (an instance role or workload identity).`
          }
        >
          <select
            aria-label="Cloud credential"
            className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
            value={config.cloud_credential_id ?? ""}
            onChange={(e) => setConfig("cloud_credential_id", e.target.value)}
            disabled={disabled}
          >
            <option value="">(none — use the runner's identity)</option>
            {matchingCredentials.map((c) => (
              <option key={c.id} value={c.id}>
                {managed ? `${c.name} (${PROVIDER_NAMES[c.provider]})` : c.name}
              </option>
            ))}
          </select>
        </Field>

        {/* Not a deploy target (terraform hides the target-cluster field): this
            attaches ready-to-use auth for a registered cluster so the module's
            kubernetes/helm/kubectl providers work with no provider config in
            code. Stored as config.auth_cluster_id. */}
        <Field
          label="Cluster authentication"
          help="Optional — gives this run ready-to-use authentication for a registered cluster, for modules that create Kubernetes resources. Leave the provider block in your code unconfigured; the run supplies the connection."
        >
          <select
            className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
            value={config.auth_cluster_id ?? ""}
            onChange={(e) => setConfig("auth_cluster_id", e.target.value)}
            disabled={disabled}
          >
            <option value="">(none)</option>
            {clusters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
      </Section>
      <Section title="Advanced" collapsible summary={advancedSummary}>
        {/* Workspace (cloud backends only — managed state takes none). Selected,
            created on first use, after init for every unit of the component.
            Stored as config.workspace. */}
        {!managed && (
          <Field
            label="Workspace"
            help="The OpenTofu workspace to run in, for state that lives in a non-default one or components sharing one bucket and key. Leave empty for the default."
          >
            <input
              type="text"
              className="w-full border border-neutral-700 px-3 py-2 font-mono text-sm"
              placeholder="default"
              value={config.workspace ?? ""}
              onChange={(e) => setConfig("workspace", e.target.value)}
              disabled={disabled}
            />
          </Field>
        )}
        <FlagsEditor
          label="Extra init flags"
          help="Optional flags appended to tofu init, one per box (e.g. -upgrade, -reconfigure)."
          value={config.init_flags}
          onChange={(v) => setConfig("init_flags", v)}
          disabled={disabled}
        />
        <FlagsEditor
          label="Extra plan flags"
          help="Optional flags appended to tofu plan, one per box (e.g. -var=env=prod, -target=aws_instance.web). Variables and targets belong here — apply re-uses this reviewed plan."
          value={config.plan_flags}
          onChange={(v) => setConfig("plan_flags", v)}
          disabled={disabled}
        />
        <FlagsEditor
          label="Extra apply flags"
          help="Optional flags appended to tofu apply, one per box (e.g. -parallelism=20). Apply uses the saved plan, so plan-time flags like -var have no effect here."
          value={config.apply_flags}
          onChange={(v) => setConfig("apply_flags", v)}
          disabled={disabled}
        />
      </Section>
    </>
  );
}

// OutputRefButtons is the insert-a-reference helper under the helm values and
// namespace fields: one button per upstream OpenTofu component, appending a
// ${{ components.<name>.outputs. }} stub (the user completes the key) to the
// field. Renders nothing when there is no upstream OpenTofu component or the
// editor is read-only.
function OutputRefButtons({
  names,
  onInsert,
  disabled,
}: {
  names: string[];
  onInsert: (snippet: string) => void;
  disabled: boolean;
}) {
  if (disabled || names.length === 0) return null;
  return (
    <p className="mt-1 text-xs text-neutral-400">
      Insert an output reference:{" "}
      {names.map((name) => (
        <button
          key={name}
          type="button"
          onClick={() => onInsert(outputsRefSnippet(name))}
          className="mr-1 border border-neutral-700 px-1.5 py-0.5 font-mono text-[11px] text-neutral-300 hover:border-neutral-500 hover:text-white"
        >
          {name}
        </button>
      ))}
    </p>
  );
}

// BACKEND_PROVIDER maps a state backend to the cloud-credential provider that
// authenticates to it (the same credential also serves the module's providers
// for that cloud).
const BACKEND_PROVIDER: Record<string, CloudCredential["provider"]> = {
  s3: "aws",
  gcs: "gcp",
  azurerm: "azure",
};

const PROVIDER_NAMES: Record<CloudCredential["provider"], string> = {
  aws: "AWS",
  gcp: "Google Cloud",
  azure: "Azure",
};

// parseFlags defensively parses the JSON string-array a terraform node stores
// under an {init,plan,apply}_flags key into a flat list of flag tokens. A
// missing/invalid value yields no rows (the editor starts empty).
function parseFlags(raw: string | undefined): string[] {
  if (!raw || raw.trim() === "") return [];
  try {
    const arr = JSON.parse(raw) as unknown;
    if (!Array.isArray(arr)) return [];
    return arr.map((v) => (v == null ? "" : String(v)));
  } catch {
    return [];
  }
}

// serializeFlags turns the flag tokens back into a JSON string-array, dropping
// blank entries. An empty list serializes to "" so the key is omitted on save.
function serializeFlags(flags: string[]): string {
  const kept = flags.filter((f) => f.trim() !== "");
  if (kept.length === 0) return "";
  return JSON.stringify(kept);
}

// useDraftRows keeps a row-list editor's rows in local state so a just-added
// blank row can exist on screen even though the serializer drops blanks —
// deriving rows from the serialized value on every render made "+ Add" a
// silent no-op (the new empty row vanished in the serialize/parse round-trip).
// It resyncs from `value` only when a change didn't come from this editor
// (e.g. the page switches to a different node). `commit` stores the next rows
// locally and returns their serialized form for the caller to emit.
function useDraftRows<T>(
  value: string | undefined,
  parse: (raw: string | undefined) => T[],
  serialize: (rows: T[]) => string,
): [T[], (next: T[]) => string] {
  const [rows, setRows] = useState<T[]>(() => parse(value));
  const lastEmitted = useRef(value ?? "");
  useEffect(() => {
    if ((value ?? "") !== lastEmitted.current) {
      lastEmitted.current = value ?? "";
      setRows(parse(value));
    }
  }, [value, parse]);
  function commit(next: T[]): string {
    setRows(next);
    const serialized = serialize(next);
    lastEmitted.current = serialized;
    return serialized;
  }
  return [rows, commit];
}

// tfvarsProblem reports why a typed-inputs value is not a JSON object of
// variable name → value, or null when it is (or is empty). The same shape
// the server enforces, checked here so the editor can flag it inline.
function tfvarsProblem(raw: string): string | null {
  if (raw.trim() === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "Not valid JSON.";
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return "Must be a JSON object: { \"name\": value, … }.";
  }
  const bad = Object.keys(parsed).find((k) => !/^[A-Za-z_][A-Za-z0-9_-]*$/.test(k));
  return bad === undefined ? null : `"${bad}" is not a valid variable name.`;
}

// TFVarsEditor edits the typed root-module inputs of an OpenTofu component:
// one JSON object (variable name → any JSON value) the run writes into the
// module as spacefleet.auto.tfvars.json, so lists, maps, and objects are
// authored as JSON rather than HCL-in-a-string. Stored as-is in
// config.tfvars (the server validates the object shape); an inline note
// flags JSON that would be rejected. Configuration only — the file is part of
// the run's script — so secrets belong in Variables instead.
function TFVarsEditor({
  value,
  onChange,
  disabled,
}: {
  value: string | undefined;
  onChange: (raw: string) => void;
  disabled: boolean;
}) {
  const raw = value ?? "";
  const problem = tfvarsProblem(raw);
  return (
    <Field
      label="Input variables"
      help="Not for secrets — put those in Variables, marked sensitive."
    >
      <textarea
        aria-label="Input variables"
        className="w-full border border-neutral-700 px-3 py-2 font-mono text-sm"
        rows={4}
        placeholder={'{\n  "region": "eu-west-1",\n  "replicas": 3,\n  "tags": { "team": "core" }\n}'}
        value={raw}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        spellCheck={false}
      />
      {problem && <p className="mt-1 text-xs text-red-400">{problem}</p>}
    </Field>
  );
}

// FlagsEditor edits an ordered list of CLI flag tokens, serialized to the JSON
// string-array a terraform node stores in config (init_flags/plan_flags/
// apply_flags). One whole flag per box (e.g. "-var=env=prod"); a flag taking a
// separate value is two boxes ("-var-file", "prod.tfvars") or one "=" box.
// Sharp corners, neutral palette per brand.
function FlagsEditor({
  label,
  help,
  value,
  onChange,
  disabled,
}: {
  label: string;
  help?: string;
  value: string | undefined;
  onChange: (serialized: string) => void;
  disabled: boolean;
}) {
  const [flags, commit] = useDraftRows(value, parseFlags, serializeFlags);
  function emit(next: string[]) {
    onChange(commit(next));
  }
  return (
    <div>
      <p className="mb-1 text-sm font-medium text-neutral-300">{label}</p>
      {help && <p className="mb-2 text-xs text-neutral-400">{help}</p>}
      {flags.length === 0 ? (
        <p className="text-xs text-neutral-400">No flags.</p>
      ) : (
        <ol className="space-y-2">
          {flags.map((flag, i) => (
            <li key={i} className="flex gap-1">
              <input
                type="text"
                aria-label={`Flag ${i + 1}`}
                className="flex-1 border border-neutral-700 px-2 py-1 font-mono text-xs"
                placeholder="-var=env=prod"
                value={flag}
                onChange={(e) =>
                  emit(flags.map((f, j) => (j === i ? e.target.value : f)))
                }
                disabled={disabled}
              />
              {!disabled && (
                <button
                  type="button"
                  onClick={() => emit(flags.filter((_, j) => j !== i))}
                  className="px-2 text-xs text-neutral-400 hover:text-red-400"
                >
                  Remove
                </button>
              )}
            </li>
          ))}
        </ol>
      )}
      {!disabled && (
        <button
          type="button"
          onClick={() => emit([...flags, ""])}
          className="mt-2 text-sm font-medium text-neutral-300 hover:text-white"
        >
          + Add flag
        </button>
      )}
    </div>
  );
}

// ValuesSourcesEditor edits the ordered list of git value sources that the
// helm component serializes into the config.values_sources JSON string.
function ValuesSourcesEditor({
  value,
  onChange,
  canPickRepo,
  onPickRepo,
  disabled,
}: {
  value: string | undefined;
  onChange: (serialized: string) => void;
  canPickRepo: boolean;
  onPickRepo: (serialized: string, repo: GitHubRepository) => void;
  disabled: boolean;
}) {
  const [rows, commit] = useDraftRows(
    value,
    parseValuesSources,
    serializeValuesSources,
  );
  function emit(next: ValuesSourceRow[]) {
    onChange(commit(next));
  }
  function update(i: number, key: keyof ValuesSourceRow, v: string) {
    emit(rows.map((r, j) => (j === i ? { ...r, [key]: v } : r)));
  }
  // A picked repo goes through commit too, then up via onPickRepo so the
  // parent can pair the rows with the repo's installation in one update.
  function pickRepo(i: number, repo: GitHubRepository) {
    const next = rows.map((r, j) =>
      j === i ? { ...r, repo_url: repo.clone_url } : r,
    );
    onPickRepo(commit(next), repo);
  }
  return (
    <div>
      <p className="mb-1 text-sm font-medium text-neutral-300">
        Values from Git
      </p>
      <p className="mb-2 text-xs text-neutral-400">
        Optional value files pulled from Git, applied in order before the inline
        values above.
      </p>
      {rows.length === 0 ? (
        <p className="text-xs text-neutral-400">No git values sources.</p>
      ) : (
        <ol className="space-y-2">
          {rows.map((src, i) => (
            <li key={i} className="border border-neutral-800 bg-neutral-800/50 p-2">
              <div className="mb-1 flex items-center justify-between">
                <span className="text-xs font-medium text-neutral-400">
                  Source {i + 1}
                </span>
                {!disabled && (
                  <button
                    type="button"
                    onClick={() => emit(rows.filter((_, j) => j !== i))}
                    className="text-xs text-neutral-400 hover:text-red-400"
                  >
                    Remove
                  </button>
                )}
              </div>
              <div className="mb-1">
                <WithPicker
                  picker={
                    canPickRepo && !disabled ? (
                      <RepositoryPicker
                        label="Select repository"
                        onSelect={(repo) => pickRepo(i, repo)}
                      />
                    ) : null
                  }
                >
                  <input
                    type="text"
                    aria-label={`Source ${i + 1} repository URL`}
                    className="w-full border border-neutral-700 px-2 py-1 text-xs"
                    placeholder="https://github.com/org/config.git"
                    value={src.repo_url}
                    onChange={(e) => update(i, "repo_url", e.target.value)}
                    disabled={disabled}
                  />
                </WithPicker>
              </div>
              <div className="flex gap-1">
                <input
                  type="text"
                  aria-label={`Source ${i + 1} branch or tag`}
                  className="w-1/3 border border-neutral-700 px-2 py-1 text-xs"
                  placeholder="ref"
                  value={src.git_ref ?? ""}
                  onChange={(e) => update(i, "git_ref", e.target.value)}
                  disabled={disabled}
                />
                <input
                  type="text"
                  aria-label={`Source ${i + 1} values file path`}
                  className="flex-1 border border-neutral-700 px-2 py-1 text-xs"
                  placeholder="envs/prod/values.yaml"
                  value={src.path}
                  onChange={(e) => update(i, "path", e.target.value)}
                  disabled={disabled}
                />
              </div>
            </li>
          ))}
        </ol>
      )}
      {!disabled && (
        <button
          type="button"
          onClick={() => emit([...rows, { repo_url: "", path: "" }])}
          className="mt-2 text-sm font-medium text-neutral-300 hover:text-white"
        >
          + Add values source
        </button>
      )}
    </div>
  );
}

// WithPicker sets a repository URL input beside its picker button, or renders
// the input alone when the picker isn't available.
function WithPicker({
  picker,
  children,
}: {
  picker: ReactNode;
  children: ReactNode;
}) {
  if (!picker) return <>{children}</>;
  return (
    <div className="flex gap-2 *:min-w-0">
      {children}
      {picker}
    </div>
  );
}

// nameHelp says where a component's name shows up: an OpenTofu component's
// outputs are referenced by it, and a Helm release is named after it.
function nameHelp(type: ComponentType): string | undefined {
  switch (type) {
    case "terraform":
      return "Used in ${{ components.<name>.outputs.* }} references.";
    case "helm":
      return "Seeds the Helm release name.";
    default:
      return undefined;
  }
}

// Section groups related fields under a heading, set apart from the fields
// before it (the first one sits flush with the top of the form). A collapsible section starts closed, its heading a toggle that
// shows the summary (what's set inside) while closed.
function Section({
  title,
  collapsible = false,
  summary,
  children,
}: {
  title: string;
  collapsible?: boolean;
  summary?: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(!collapsible);
  const heading = "text-base font-semibold tracking-tight text-neutral-100";
  return (
    <section className="space-y-4 pt-6 first:pt-0">
      {collapsible ? (
        <h3 className={heading}>
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            className="inline-flex items-center gap-1.5 hover:text-neutral-300"
          >
            {open ? (
              <ChevronDown className="h-4 w-4 text-neutral-500" />
            ) : (
              <ChevronRight className="h-4 w-4 text-neutral-500" />
            )}
            {title}
            {!open && summary && (
              <span className="ml-1 text-sm font-normal tracking-normal text-neutral-400">
                {summary}
              </span>
            )}
          </button>
        </h3>
      ) : (
        <h3 className={heading}>{title}</h3>
      )}
      {open && children}
    </section>
  );
}

function Field({
  label,
  help,
  children,
}: {
  label?: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      {label && (
        <label className="mb-1 block text-sm font-medium text-neutral-300">
          {label}
        </label>
      )}
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-400">{help}</p>}
    </div>
  );
}
