import type { ReactNode } from "react";
import type { components } from "../../api/schema";
import { CHART_SOURCES } from "../chartSources";
import {
  TOFU_DEFAULT_VERSION,
  TOFU_VERSIONS,
  tofuNativeLock,
} from "../../lib/tofuVersions";
import { RepositoryPicker } from "./RepositoryPicker";
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

// EditableComponent is the canvas's working copy of one node — the fields the
// editor edits. position/depends_on/group_id live on the React Flow node + edges,
// not here, so the builder assembles them at save time.
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
  target_cluster_id: string | null;
  target_namespace: string;
  chart_credential_id: string | null;
  github_installation_id: string | null;
}

interface ComponentFieldsProps {
  component: EditableComponent;
  onChange: (next: EditableComponent) => void;
  clusters: Cluster[];
  credentials: ChartCredential[];
  cloudCredentials: CloudCredential[];
  installations: GitHubInstallation[];
  githubEnabled: boolean;
  // When true the fields render read-only (a viewer opened the editor). Inputs
  // are disabled rather than hidden so the configuration stays inspectable.
  disabled?: boolean;
}

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
}: ComponentFieldsProps) {
  function set<K extends keyof EditableComponent>(key: K, value: EditableComponent[K]) {
    onChange({ ...component, [key]: value });
  }
  function setConfig(key: string, value: string) {
    onChange({ ...component, config: { ...component.config, [key]: value } });
  }

  const chartSource = (component.config.chart_source as ChartSource) || "http_repo";
  const valuesRows = parseValuesSources(component.config.values_sources);

  function setValuesRows(rows: ValuesSourceRow[]) {
    onChange({
      ...component,
      config: { ...component.config, values_sources: serializeValuesSources(rows) },
    });
  }

  // A repository can be picked when the GitHub App is configured and the org has
  // at least one installation; otherwise the user types the URL by hand.
  const canPickRepo = githubEnabled && installations.length > 0;

  // pickRepoInto sets a config repo-URL field to the picked clone URL and, in the
  // same update, selects the matching GitHub installation on the component — the
  // whole point of the picker is that the two stay in sync.
  function pickRepoInto(repoUrlKey: string) {
    return (repo: GitHubRepository) =>
      onChange({
        ...component,
        config: { ...component.config, [repoUrlKey]: repo.clone_url },
        github_installation_id: repo.installation_id,
      });
  }

  // pickValuesSourceRepo is the same idea for a git values source row: it must
  // update the row's repo_url and the component-level installation in one
  // onChange so neither change clobbers the other.
  function pickValuesSourceRepo(rowIndex: number, repo: GitHubRepository) {
    const rows = valuesRows.map((r, i) =>
      i === rowIndex ? { ...r, repo_url: repo.clone_url } : r,
    );
    onChange({
      ...component,
      config: { ...component.config, values_sources: serializeValuesSources(rows) },
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
      <Field label="Name">
        <input
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          value={component.name}
          onChange={(e) => set("name", e.target.value)}
          placeholder="component name"
          disabled={disabled}
        />
      </Field>

      {component.type === "helm" ? (
        <HelmConfig
          config={component.config}
          chartSource={chartSource}
          setConfig={setConfig}
          valuesRows={valuesRows}
          setValuesRows={setValuesRows}
          repoUrlPicker={repoUrlPicker}
          canPickRepo={canPickRepo}
          onPickValuesSourceRepo={pickValuesSourceRepo}
          disabled={disabled}
        />
      ) : component.type === "terraform" ? (
        <TerraformConfig
          config={component.config}
          setConfig={setConfig}
          repoUrlPicker={repoUrlPicker}
          cloudCredentials={cloudCredentials}
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

      {/* Credentials. Helm http_repo/oci take a chart credential; git sources
          (helm git chart, or any git-sourced values, or a manifest) take a
          GitHub installation for private clones. */}
      {component.type === "helm" &&
        (chartSource === "http_repo" || chartSource === "oci") && (
          <Field
            label="Chart credential"
            help="Optional — only for a private repo/registry."
          >
            <select
              className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
              value={component.chart_credential_id ?? ""}
              onChange={(e) => set("chart_credential_id", e.target.value || null)}
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

      {githubEnabled && (
        <Field
          label="GitHub installation"
          help="Optional — only for a private Git source."
        >
          <select
            className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
            value={component.github_installation_id ?? ""}
            onChange={(e) => set("github_installation_id", e.target.value || null)}
            disabled={disabled}
          >
            <option value="">None (public repository)</option>
            {installations.map((inst) => (
              <option key={inst.id} value={inst.id}>
                {inst.account_login || inst.installation_id}
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
            className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
          help="The namespace this release deploys into."
        >
          <input
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            value={component.target_namespace}
            onChange={(e) => set("target_namespace", e.target.value)}
            disabled={disabled}
          />
        </Field>
      )}

      <label className="flex items-center gap-2 text-sm text-neutral-700">
        <input
          type="checkbox"
          className="h-4 w-4 accent-black"
          checked={component.continue_on_failure}
          onChange={(e) => set("continue_on_failure", e.target.checked)}
          disabled={disabled}
        />
        Continue on failure
      </label>

      {/* Approval gate. An OpenTofu node frames it as an Auto-approve opt-out
          (gated by default — the run plans, then pauses for a human to review
          before applying); every other node frames it as an opt-in manual-
          approval requirement. Both edit the same requires_approval flag. */}
      {component.type === "terraform" ? (
        <Field help="Off (the default): the run plans, then pauses for a human to review the plan and approve before applying.">
          <label className="flex items-center gap-2 text-sm text-neutral-700">
            <input
              type="checkbox"
              className="h-4 w-4 accent-black"
              checked={!component.requires_approval}
              onChange={(e) => set("requires_approval", !e.target.checked)}
              disabled={disabled}
            />
            Auto-approve apply
          </label>
        </Field>
      ) : (
        <Field help="When on, the run pauses at this step until a human approves it.">
          <label className="flex items-center gap-2 text-sm text-neutral-700">
            <input
              type="checkbox"
              className="h-4 w-4 accent-black"
              checked={component.requires_approval}
              onChange={(e) => set("requires_approval", e.target.checked)}
              disabled={disabled}
            />
            Require manual approval
          </label>
        </Field>
      )}
    </div>
  );
}

function HelmConfig({
  config,
  chartSource,
  setConfig,
  valuesRows,
  setValuesRows,
  repoUrlPicker,
  canPickRepo,
  onPickValuesSourceRepo,
  disabled,
}: {
  config: Record<string, string>;
  chartSource: ChartSource;
  setConfig: (key: string, value: string) => void;
  valuesRows: ValuesSourceRow[];
  setValuesRows: (rows: ValuesSourceRow[]) => void;
  repoUrlPicker: ReactNode;
  canPickRepo: boolean;
  onPickValuesSourceRepo: (rowIndex: number, repo: GitHubRepository) => void;
  disabled: boolean;
}) {
  const source = CHART_SOURCES.find((s) => s.value === chartSource) ?? CHART_SOURCES[0];
  return (
    <>
      <Field label="Chart source">
        <select
          className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
          {field.key === "repo_url" && chartSource === "git" && repoUrlPicker && (
            <div className="mb-1.5">{repoUrlPicker}</div>
          )}
          <input
            type="text"
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            placeholder={field.placeholder}
            value={config[field.key] ?? ""}
            onChange={(e) => setConfig(field.key, e.target.value)}
            disabled={disabled}
          />
        </Field>
      ))}

      <Field label="Release name" help="Defaults to the component name.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="(defaults to name)"
          value={config.release_name ?? ""}
          onChange={(e) => setConfig("release_name", e.target.value)}
          disabled={disabled}
        />
      </Field>

      <Field label="Values (values.yaml)" help="Optional inline overrides.">
        <textarea
          className="h-32 w-full border border-neutral-300 px-3 py-2 font-mono text-xs"
          placeholder={"replicaCount: 2\n"}
          value={config.values ?? ""}
          onChange={(e) => setConfig("values", e.target.value)}
          disabled={disabled}
        />
      </Field>

      <ValuesSourcesEditor
        rows={valuesRows}
        onChange={setValuesRows}
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
        {repoUrlPicker && <div className="mb-1.5">{repoUrlPicker}</div>}
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="https://github.com/org/manifests.git"
          value={config.repo_url ?? ""}
          onChange={(e) => setConfig("repo_url", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Branch or tag" help="Default branch if empty.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="(default branch)"
          value={config.git_ref ?? ""}
          onChange={(e) => setConfig("git_ref", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Path" help="File or directory to kubectl apply.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
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
// in place). S3 is the only supported backend type today; its settings are
// dedicated fields that serialize into the config.backend_config JSON object
// the server validates and renders. backend_config gets the same redaction
// treatment as helm inline values (see lib/api workflow secret keys), so
// non-editors see blank fields.
function TerraformConfig({
  config,
  setConfig,
  repoUrlPicker,
  cloudCredentials,
  disabled,
}: {
  config: Record<string, string>;
  setConfig: (key: string, value: string) => void;
  repoUrlPicker: ReactNode;
  cloudCredentials: CloudCredential[];
  disabled: boolean;
}) {
  const s3 = parseBackendConfig(config.backend_config);
  const encrypt = s3.encrypt === "true";

  // The OpenTofu line this component runs (absent = the server's default
  // line) decides the locking story below: 1.10+ locks state natively in the
  // bucket — automatic, nothing to set up — while 1.9 locks via a DynamoDB
  // table.
  const tofuVersion = config.tofu_version || TOFU_DEFAULT_VERSION;
  const nativeLock = tofuNativeLock(config.tofu_version);

  // Only aws credentials can be injected into the OpenTofu run for now.
  const awsCredentials = cloudCredentials.filter((c) => c.provider === "aws");

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
      <Field label="Repository URL">
        {repoUrlPicker && <div className="mb-1.5">{repoUrlPicker}</div>}
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="https://github.com/org/infra.git"
          value={config.repo_url ?? ""}
          onChange={(e) => setConfig("repo_url", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Branch or tag" help="Default branch if empty.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="(default branch)"
          value={config.git_ref ?? ""}
          onChange={(e) => setConfig("git_ref", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Working path" help="Directory holding the OpenTofu files.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="infra/prod"
          value={config.path ?? ""}
          onChange={(e) => setConfig("path", e.target.value)}
          disabled={disabled}
        />
      </Field>

      <Field
        label="OpenTofu version"
        help="The OpenTofu release this component runs. 1.10 and newer lock state automatically — no lock table needed."
      >
        <select
          className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
        help="Where this component's OpenTofu state lives. Spacefleet configures your module to use it at init (overriding any backend block in code) — to adopt existing state, point it at the bucket and key your state is already in."
      >
        <select
          className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
          value={config.backend || "s3"}
          onChange={(e) => setConfig("backend", e.target.value)}
          disabled={disabled}
        >
          <option value="s3">Amazon S3</option>
        </select>
      </Field>

      <Field
        label="Cloud credential"
        help="The AWS credential the run signs in with — used for the state bucket and your module's AWS providers. Leave as instance role to use the runner's own role."
      >
        <select
          className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
          value={config.cloud_credential_id ?? ""}
          onChange={(e) => setConfig("cloud_credential_id", e.target.value)}
          disabled={disabled}
        >
          <option value="">(none — use instance role)</option>
          {awsCredentials.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </Field>

      <Field label="Bucket" help="The S3 bucket holding the state.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
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
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="envs/prod/terraform.tfstate"
          value={s3.key ?? ""}
          onChange={(e) => setS3("key", e.target.value)}
          disabled={disabled}
        />
      </Field>
      <Field label="Region" help="AWS region of the bucket.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="us-east-1"
          value={s3.region ?? ""}
          onChange={(e) => setS3("region", e.target.value)}
          disabled={disabled}
        />
      </Field>
      {nativeLock ? (
        <>
          <div className="border border-neutral-200 bg-neutral-50 px-3 py-2 text-sm text-neutral-600">
            <span className="font-medium text-neutral-700">
              State locking is automatic.
            </span>{" "}
            OpenTofu {tofuVersion} locks state in the bucket itself during
            every plan and apply, so concurrent runs can't corrupt it — there's
            nothing to set up.
          </div>
          <Field
            label="DynamoDB lock table"
            help="Optional. Only needed while this state is also used outside Spacefleet with DynamoDB locking — both locks are held, so you can migrate off the table safely."
          >
            <input
              type="text"
              className="w-full border border-neutral-300 px-3 py-2 text-sm"
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
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            placeholder="(no locking)"
            value={s3.dynamodb_table ?? ""}
            onChange={(e) => setS3("dynamodb_table", e.target.value)}
            disabled={disabled}
          />
        </Field>
      )}

      <label className="flex items-center gap-2 text-sm text-neutral-700">
        <input
          type="checkbox"
          className="h-4 w-4 accent-black"
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
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            placeholder="(SSE-S3)"
            value={s3.kms_key_id ?? ""}
            onChange={(e) => setS3("kms_key_id", e.target.value)}
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
    </>
  );
}

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
  const flags = parseFlags(value);
  function emit(next: string[]) {
    onChange(serializeFlags(next));
  }
  return (
    <div>
      <p className="mb-1 text-sm font-medium text-neutral-700">{label}</p>
      {help && <p className="mb-2 text-xs text-neutral-500">{help}</p>}
      {flags.length === 0 ? (
        <p className="text-xs text-neutral-500">No flags.</p>
      ) : (
        <ol className="space-y-2">
          {flags.map((flag, i) => (
            <li key={i} className="flex gap-1">
              <input
                type="text"
                aria-label={`Flag ${i + 1}`}
                className="flex-1 border border-neutral-300 px-2 py-1 font-mono text-xs"
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
                  className="px-2 text-xs text-neutral-500 hover:text-red-600"
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
          className="mt-2 text-sm font-medium text-neutral-700 hover:text-black"
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
  rows,
  onChange,
  canPickRepo,
  onPickRepo,
  disabled,
}: {
  rows: ValuesSourceRow[];
  onChange: (rows: ValuesSourceRow[]) => void;
  canPickRepo: boolean;
  onPickRepo: (rowIndex: number, repo: GitHubRepository) => void;
  disabled: boolean;
}) {
  function update(i: number, key: keyof ValuesSourceRow, value: string) {
    onChange(rows.map((r, j) => (j === i ? { ...r, [key]: value } : r)));
  }
  return (
    <div>
      <p className="mb-1 text-sm font-medium text-neutral-700">Values from Git</p>
      <p className="mb-2 text-xs text-neutral-500">
        Optional value files pulled from Git, applied in order before the inline
        values above.
      </p>
      {rows.length === 0 ? (
        <p className="text-xs text-neutral-500">No git values sources.</p>
      ) : (
        <ol className="space-y-2">
          {rows.map((src, i) => (
            <li key={i} className="border border-neutral-200 bg-neutral-50 p-2">
              <div className="mb-1 flex items-center justify-between">
                <span className="text-xs font-medium text-neutral-500">
                  Source {i + 1}
                </span>
                {!disabled && (
                  <button
                    type="button"
                    onClick={() => onChange(rows.filter((_, j) => j !== i))}
                    className="text-xs text-neutral-500 hover:text-red-600"
                  >
                    Remove
                  </button>
                )}
              </div>
              {canPickRepo && !disabled && (
                <div className="mb-1">
                  <RepositoryPicker
                    label="Select repository"
                    onSelect={(repo) => onPickRepo(i, repo)}
                  />
                </div>
              )}
              <input
                type="text"
                aria-label={`Source ${i + 1} repository URL`}
                className="mb-1 w-full border border-neutral-300 px-2 py-1 text-xs"
                placeholder="https://github.com/org/config.git"
                value={src.repo_url}
                onChange={(e) => update(i, "repo_url", e.target.value)}
                disabled={disabled}
              />
              <div className="flex gap-1">
                <input
                  type="text"
                  aria-label={`Source ${i + 1} branch or tag`}
                  className="w-1/3 border border-neutral-300 px-2 py-1 text-xs"
                  placeholder="ref"
                  value={src.git_ref ?? ""}
                  onChange={(e) => update(i, "git_ref", e.target.value)}
                  disabled={disabled}
                />
                <input
                  type="text"
                  aria-label={`Source ${i + 1} values file path`}
                  className="flex-1 border border-neutral-300 px-2 py-1 text-xs"
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
          onClick={() => onChange([...rows, { repo_url: "", path: "" }])}
          className="mt-2 text-sm font-medium text-neutral-700 hover:text-black"
        >
          + Add values source
        </button>
      )}
    </div>
  );
}

// parseBackendConfig defensively parses the JSON-object string the terraform
// node stores in config.backend_config into a flat string map. A
// missing/invalid value yields an empty object (the fields start blank).
function parseBackendConfig(raw: string | undefined): Record<string, string> {
  if (!raw || raw.trim() === "") return {};
  try {
    const obj = JSON.parse(raw) as unknown;
    if (!obj || typeof obj !== "object" || Array.isArray(obj)) return {};
    return Object.fromEntries(
      Object.entries(obj as Record<string, unknown>).map(([key, value]) => [
        key,
        value == null ? "" : String(value),
      ]),
    );
  } catch {
    return {};
  }
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
        <label className="mb-1 block text-sm font-medium text-neutral-700">
          {label}
        </label>
      )}
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-500">{help}</p>}
    </div>
  );
}
