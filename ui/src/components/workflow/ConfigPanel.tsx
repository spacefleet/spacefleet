import { Trash2, X } from "lucide-react";
import type { components } from "../../api/schema";
import { CHART_SOURCES } from "../chartSources";
import {
  parseValuesSources,
  serializeValuesSources,
  type ValuesSourceRow,
} from "./valuesSources";

type ComponentType = components["schemas"]["ComponentType"];
type ChartSource = components["schemas"]["ChartSource"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];

// EditableComponent is the canvas's working copy of one node — the fields the
// config panel edits. position/depends_on live on the React Flow node + edges,
// not here, so the builder assembles them at save time.
export interface EditableComponent {
  id: string;
  name: string;
  type: ComponentType;
  config: Record<string, string>;
  continue_on_failure: boolean;
  target_cluster_id: string | null;
  target_namespace: string;
  chart_credential_id: string | null;
  github_installation_id: string | null;
}

interface ConfigPanelProps {
  component: EditableComponent;
  onChange: (next: EditableComponent) => void;
  onDelete: () => void;
  onClose: () => void;
  clusters: Cluster[];
  credentials: ChartCredential[];
  installations: GitHubInstallation[];
  githubEnabled: boolean;
}

// ConfigPanel is the side panel for editing the selected node. Type-specific
// config fields (helm: chart source + repo/chart/version/git + values + a
// values-sources editor; manifest: repo/ref/path), continue-on-failure, target
// overrides, and credential/installation pickers. Client-side validation is
// intentionally light — the server (PUT /workflow) is the source of truth.
export function ConfigPanel({
  component,
  onChange,
  onDelete,
  onClose,
  clusters,
  credentials,
  installations,
  githubEnabled,
}: ConfigPanelProps) {
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

  return (
    <aside className="flex h-full w-96 shrink-0 flex-col overflow-y-auto border-l border-neutral-200 bg-white">
      <div className="flex items-start justify-between gap-2 border-b border-neutral-200 px-4 py-3">
        <div>
          <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            {component.type} component
          </p>
          <h2 className="mt-0.5 text-sm font-semibold text-neutral-900">
            Edit node
          </h2>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close panel"
          className="p-1 text-neutral-400 hover:text-neutral-900"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="flex-1 space-y-4 px-4 py-4">
        <Field label="Name">
          <input
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            value={component.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="component name"
          />
        </Field>

        {component.type === "helm" ? (
          <HelmConfig
            config={component.config}
            chartSource={chartSource}
            setConfig={setConfig}
            valuesRows={valuesRows}
            setValuesRows={setValuesRows}
          />
        ) : (
          <ManifestConfig config={component.config} setConfig={setConfig} />
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

        {/* Targeting overrides. Blank = the application's default target. */}
        <Field
          label="Target cluster override"
          help="Optional — defaults to the application's target cluster."
        >
          <select
            className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
            value={component.target_cluster_id ?? ""}
            onChange={(e) => set("target_cluster_id", e.target.value || null)}
          >
            <option value="">App default</option>
            {clusters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>

        <Field
          label="Target namespace override"
          help="Optional — defaults to the application's target namespace."
        >
          <input
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            value={component.target_namespace}
            onChange={(e) => set("target_namespace", e.target.value)}
            placeholder="(app default)"
          />
        </Field>

        <label className="flex items-center gap-2 text-sm text-neutral-700">
          <input
            type="checkbox"
            className="h-4 w-4 accent-black"
            checked={component.continue_on_failure}
            onChange={(e) => set("continue_on_failure", e.target.checked)}
          />
          Continue on failure
        </label>
      </div>

      <div className="border-t border-neutral-200 px-4 py-3">
        <button
          type="button"
          onClick={onDelete}
          className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50"
        >
          <Trash2 className="h-3.5 w-3.5" />
          Delete node
        </button>
      </div>
    </aside>
  );
}

function HelmConfig({
  config,
  chartSource,
  setConfig,
  valuesRows,
  setValuesRows,
}: {
  config: Record<string, string>;
  chartSource: ChartSource;
  setConfig: (key: string, value: string) => void;
  valuesRows: ValuesSourceRow[];
  setValuesRows: (rows: ValuesSourceRow[]) => void;
}) {
  const source = CHART_SOURCES.find((s) => s.value === chartSource) ?? CHART_SOURCES[0];
  return (
    <>
      <Field label="Chart source">
        <select
          className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
          value={chartSource}
          onChange={(e) => setConfig("chart_source", e.target.value)}
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
          <input
            type="text"
            className="w-full border border-neutral-300 px-3 py-2 text-sm"
            placeholder={field.placeholder}
            value={config[field.key] ?? ""}
            onChange={(e) => setConfig(field.key, e.target.value)}
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
        />
      </Field>

      <Field label="Values (values.yaml)" help="Optional inline overrides.">
        <textarea
          className="h-32 w-full border border-neutral-300 px-3 py-2 font-mono text-xs"
          placeholder={"replicaCount: 2\n"}
          value={config.values ?? ""}
          onChange={(e) => setConfig("values", e.target.value)}
        />
      </Field>

      <ValuesSourcesEditor rows={valuesRows} onChange={setValuesRows} />
    </>
  );
}

function ManifestConfig({
  config,
  setConfig,
}: {
  config: Record<string, string>;
  setConfig: (key: string, value: string) => void;
}) {
  return (
    <>
      <Field label="Repository URL">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="https://github.com/org/manifests.git"
          value={config.repo_url ?? ""}
          onChange={(e) => setConfig("repo_url", e.target.value)}
        />
      </Field>
      <Field label="Branch or tag" help="Default branch if empty.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="(default branch)"
          value={config.git_ref ?? ""}
          onChange={(e) => setConfig("git_ref", e.target.value)}
        />
      </Field>
      <Field label="Path" help="File or directory to kubectl apply.">
        <input
          type="text"
          className="w-full border border-neutral-300 px-3 py-2 text-sm"
          placeholder="manifests/prod"
          value={config.path ?? ""}
          onChange={(e) => setConfig("path", e.target.value)}
        />
      </Field>
    </>
  );
}

// ValuesSourcesEditor edits the ordered list of git value sources that the
// helm component serializes into the config.values_sources JSON string.
function ValuesSourcesEditor({
  rows,
  onChange,
}: {
  rows: ValuesSourceRow[];
  onChange: (rows: ValuesSourceRow[]) => void;
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
                <button
                  type="button"
                  onClick={() => onChange(rows.filter((_, j) => j !== i))}
                  className="text-xs text-neutral-500 hover:text-red-600"
                >
                  Remove
                </button>
              </div>
              <input
                type="text"
                aria-label={`Source ${i + 1} repository URL`}
                className="mb-1 w-full border border-neutral-300 px-2 py-1 text-xs"
                placeholder="https://github.com/org/config.git"
                value={src.repo_url}
                onChange={(e) => update(i, "repo_url", e.target.value)}
              />
              <div className="flex gap-1">
                <input
                  type="text"
                  aria-label={`Source ${i + 1} branch or tag`}
                  className="w-1/3 border border-neutral-300 px-2 py-1 text-xs"
                  placeholder="ref"
                  value={src.git_ref ?? ""}
                  onChange={(e) => update(i, "git_ref", e.target.value)}
                />
                <input
                  type="text"
                  aria-label={`Source ${i + 1} values file path`}
                  className="flex-1 border border-neutral-300 px-2 py-1 text-xs"
                  placeholder="envs/prod/values.yaml"
                  value={src.path}
                  onChange={(e) => update(i, "path", e.target.value)}
                />
              </div>
            </li>
          ))}
        </ol>
      )}
      <button
        type="button"
        onClick={() => onChange([...rows, { repo_url: "", path: "" }])}
        className="mt-2 text-sm font-medium text-neutral-700 hover:text-black"
      >
        + Add values source
      </button>
    </div>
  );
}

function Field({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="mb-1 block text-sm font-medium text-neutral-700">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-500">{help}</p>}
    </div>
  );
}
