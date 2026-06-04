import { useEffect, useState } from "react";
import { AppWindow, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { CHART_SOURCES } from "./chartSources";

type Application = components["schemas"]["Application"];
type ChartSource = components["schemas"]["ChartSource"];
type CreateRequest = components["schemas"]["ApplicationCreateRequest"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];

// The credential type compatible with each chart source. git charts use the git
// repo's own auth, so they take no chart credential.
const CREDENTIAL_TYPE_FOR_SOURCE: Partial<
  Record<ChartSource, ChartCredential["type"]>
> = {
  http_repo: "basic_auth",
  oci: "oci",
};

// CreateApplicationDialog is a modal that registers a Helm application. It
// collects the target cluster + namespace, the runner cluster (filtered to
// job-running clusters), the chart source and its per-source fields, and an
// optional values.yaml override, then POSTs to /api/applications (which persists
// it pending — the caller starts the first rollout).
export function CreateApplicationDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (app: Application) => void;
}) {
  const [name, setName] = useState("");
  const [chartSource, setChartSource] = useState<ChartSource>("http_repo");
  const [config, setConfig] = useState<Record<string, string>>({});
  const [values, setValues] = useState("");
  const [releaseName, setReleaseName] = useState("");
  const [targetNamespace, setTargetNamespace] = useState("");
  const [targetClusterId, setTargetClusterId] = useState("");
  const [runnerClusterId, setRunnerClusterId] = useState("");
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [credentials, setCredentials] = useState<ChartCredential[]>([]);
  const [credentialId, setCredentialId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      setClusters(data ?? []);
    })();
    void (async () => {
      const { data } = await api.GET("/api/chart-credentials");
      setCredentials(data ?? []);
    })();
  }, []);

  const selected = CHART_SOURCES.find((s) => s.value === chartSource)!;
  // Only job-running clusters can host a rollout's TaskRun.
  const runnerClusters = clusters.filter((c) => c.runs_jobs);
  // Credentials compatible with the selected source (none for git). A selected
  // credential is cleared when it no longer matches after a source change.
  const credentialType = CREDENTIAL_TYPE_FOR_SOURCE[chartSource];
  const compatibleCredentials = credentials.filter(
    (c) => c.type === credentialType,
  );

  function setField(key: string, value: string) {
    setConfig((c) => ({ ...c, [key]: value }));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);

    const cfg: Record<string, string> = {};
    for (const field of selected.fields) {
      const raw = (config[field.key] ?? "").trim();
      if (raw !== "") cfg[field.key] = raw;
    }
    const body: CreateRequest = {
      name: name.trim(),
      chart_source: chartSource,
      config: cfg,
      target_namespace: targetNamespace.trim(),
      target_cluster_id: targetClusterId,
      runner_cluster_id: runnerClusterId,
    };
    if (values.trim() !== "") body.values = values;
    if (releaseName.trim() !== "") body.release_name = releaseName.trim();
    if (credentialId !== "") body.chart_credential_id = credentialId;

    const { data, error } = await api.POST("/api/applications", { body });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not create application");
      return;
    }
    onCreated(data);
  }

  const ready =
    name.trim() !== "" &&
    targetNamespace.trim() !== "" &&
    targetClusterId !== "" &&
    runnerClusterId !== "";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-lg border border-gray-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-gray-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <AppWindow className="h-5 w-5 text-gray-500" />
            New Helm application
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-gray-400 hover:text-gray-700"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <form onSubmit={onSubmit} className="space-y-4 px-5 py-4">
          <Labeled label="Name">
            <input
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="my-app"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
          </Labeled>

          <Labeled label="Target cluster" help="The cluster the release is deployed into.">
            <select
              className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
              value={targetClusterId}
              onChange={(e) => setTargetClusterId(e.target.value)}
              required
            >
              <option value="">Select a cluster…</option>
              {clusters.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Labeled>

          <Labeled label="Target namespace">
            <input
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="default"
              value={targetNamespace}
              onChange={(e) => setTargetNamespace(e.target.value)}
              required
            />
          </Labeled>

          <Labeled
            label="Runner cluster"
            help="A job-running (Tekton-enabled) cluster the rollout runs on. For an in-cluster target, pick that same cluster."
          >
            <select
              className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
              value={runnerClusterId}
              onChange={(e) => setRunnerClusterId(e.target.value)}
              required
            >
              <option value="">Select a runner…</option>
              {runnerClusters.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
            {runnerClusters.length === 0 && (
              <p className="mt-1 text-xs text-amber-700">
                No job-running clusters yet. Enable job running (Tekton) on a
                cluster first.
              </p>
            )}
          </Labeled>

          <Labeled label="Chart source">
            <select
              className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
              value={chartSource}
              onChange={(e) => {
                setChartSource(e.target.value as ChartSource);
                setConfig({});
                setCredentialId("");
                setError(null);
              }}
            >
              {CHART_SOURCES.map((s) => (
                <option key={s.value} value={s.value}>
                  {s.label}
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-gray-500">{selected.description}</p>
          </Labeled>

          {selected.fields.map((field) => (
            <Labeled key={field.key} label={field.label} help={field.help}>
              <input
                type="text"
                className="w-full border border-gray-300 px-3 py-2 text-sm"
                placeholder={field.placeholder}
                value={config[field.key] ?? ""}
                onChange={(e) => setField(field.key, e.target.value)}
                required={field.required}
              />
            </Labeled>
          ))}

          {credentialType && (
            <Labeled
              label="Chart credential"
              help="Optional. Required only if the chart is in a private repo or registry. Manage these under Admin › Private Charts."
            >
              <select
                className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
                value={credentialId}
                onChange={(e) => setCredentialId(e.target.value)}
              >
                <option value="">None (public chart)</option>
                {compatibleCredentials.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
              {compatibleCredentials.length === 0 && (
                <p className="mt-1 text-xs text-gray-500">
                  No matching credentials yet. Add one under Admin › Private
                  Charts.
                </p>
              )}
            </Labeled>
          )}

          <Labeled
            label="Release name"
            help="Helm release name; defaults to the application name."
          >
            <input
              type="text"
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="(defaults to name)"
              value={releaseName}
              onChange={(e) => setReleaseName(e.target.value)}
            />
          </Labeled>

          <Labeled label="Values (values.yaml)" help="Optional overrides passed to helm with -f.">
            <textarea
              className="h-28 w-full border border-gray-300 px-3 py-2 font-mono text-xs"
              placeholder={"replicaCount: 2\nservice:\n  type: ClusterIP\n"}
              value={values}
              onChange={(e) => setValues(e.target.value)}
            />
          </Labeled>

          {error && <p className="text-sm text-red-600">{error}</p>}

          <div className="flex items-center justify-end gap-3 border-t border-gray-200 pt-4">
            <button
              type="button"
              onClick={onClose}
              className="text-sm text-gray-500 hover:text-gray-800"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!ready || submitting}
              className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
            >
              {submitting ? "Creating…" : "Create application"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Labeled({
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
      <label className="mb-1 block text-sm font-medium text-gray-700">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-gray-500">{help}</p>}
    </div>
  );
}
