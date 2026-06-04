import { useEffect, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { githubAppEnabled } from "../lib/appConfig";
import { CHART_SOURCES } from "../components/chartSources";

type Application = components["schemas"]["Application"];
type ChartSource = components["schemas"]["ChartSource"];
type CreateRequest = components["schemas"]["ApplicationCreateRequest"];
type UpdateRequest = components["schemas"]["ApplicationUpdateRequest"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];

// The credential type compatible with each chart source. git charts use the git
// repo's own auth, so they take no chart credential.
const CREDENTIAL_TYPE_FOR_SOURCE: Partial<
  Record<ChartSource, ChartCredential["type"]>
> = {
  http_repo: "basic_auth",
  oci: "oci",
};

// The nil UUID detaches an optional credential/installation on PATCH (see the
// ApplicationUpdateRequest schema); omitting a field instead means "no change",
// so a cleared selector must send this rather than nothing.
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

// ApplicationForm is the full-page create/edit workflow for a Helm application
// (routes /applications/new and /applications/:appId/edit — the mode is decided
// by whether appId is present). It groups the fields into sections so the form
// breathes, where the old modal crammed them together. Create POSTs then kicks
// off the first rollout and lands on the detail page; edit PATCHes the mutable
// fields and returns to the detail page. The target/runner clusters and chart
// source are fixed at registration, so they're shown read-only when editing.
export function ApplicationForm() {
  const { appId } = useParams();
  const editing = Boolean(appId);
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [name, setName] = useState("");
  const [chartSource, setChartSource] = useState<ChartSource>("http_repo");
  const [config, setConfig] = useState<Record<string, string>>({});
  const [values, setValues] = useState("");
  const [releaseName, setReleaseName] = useState("");
  const [targetNamespace, setTargetNamespace] = useState("");
  const [targetClusterId, setTargetClusterId] = useState("");
  const [runnerClusterId, setRunnerClusterId] = useState("");
  const [credentialId, setCredentialId] = useState("");
  const [installationId, setInstallationId] = useState("");

  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [credentials, setCredentials] = useState<ChartCredential[]>([]);
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);

  // In edit mode we must load the existing app before the form is meaningful.
  const [loading, setLoading] = useState(editing);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const githubEnabled = githubAppEnabled();

  // Reference data for the selectors (same as the create flow always needed).
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      setClusters(data ?? []);
    })();
    void (async () => {
      const { data } = await api.GET("/api/chart-credentials");
      setCredentials(data ?? []);
    })();
    if (githubEnabled) {
      void (async () => {
        const { data } = await api.GET("/api/github/installations");
        setInstallations(data ?? []);
      })();
    }
  }, [githubEnabled, currentOrg?.id]);

  // Prefill from the existing application when editing.
  useEffect(() => {
    if (!editing) return;
    void (async () => {
      setLoading(true);
      setLoadError(null);
      const { data, error } = await api.GET("/api/applications/{id}", {
        params: { path: { id: appId! } },
      });
      if (error || !data) {
        setLoadError(error?.message ?? "Could not load this application");
        setLoading(false);
        return;
      }
      hydrate(data);
      setLoading(false);
    })();
  }, [editing, appId, currentOrg?.id]);

  function hydrate(app: Application) {
    setName(app.name);
    setChartSource(app.chart_source);
    setConfig(app.config ?? {});
    setValues(app.values ?? "");
    setReleaseName(app.release_name ?? "");
    setTargetNamespace(app.target_namespace);
    setTargetClusterId(app.target_cluster_id);
    setRunnerClusterId(app.runner_cluster_id);
    setCredentialId(app.chart_credential_id ?? "");
    setInstallationId(app.github_installation_id ?? "");
  }

  const selected = CHART_SOURCES.find((s) => s.value === chartSource)!;
  // Only job-running clusters can host a rollout's TaskRun.
  const runnerClusters = clusters.filter((c) => c.runs_jobs);
  // Credentials compatible with the selected source (none for git).
  const credentialType = CREDENTIAL_TYPE_FOR_SOURCE[chartSource];
  const compatibleCredentials = credentials.filter(
    (c) => c.type === credentialType,
  );
  const clusterName = (id: string) =>
    clusters.find((c) => c.id === id)?.name ?? id;

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

    if (editing) {
      const body: UpdateRequest = {
        name: name.trim(),
        config: cfg,
        values,
        release_name: releaseName.trim(),
        target_namespace: targetNamespace.trim(),
        // A cleared selector detaches via the nil UUID; an absent selector
        // (wrong source type / GitHub disabled) leaves the field untouched.
        chart_credential_id: credentialType
          ? credentialId || NIL_UUID
          : undefined,
        github_installation_id:
          chartSource === "git" && githubEnabled
            ? installationId || NIL_UUID
            : undefined,
      };
      const { error } = await api.PATCH("/api/applications/{id}", {
        params: { path: { id: appId! } },
        body,
      });
      setSubmitting(false);
      if (error) {
        setError(error.message ?? "Could not save changes");
        return;
      }
      navigate(`/applications/${appId}`);
      return;
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
    if (chartSource === "git" && installationId !== "")
      body.github_installation_id = installationId;

    const { data, error } = await api.POST("/api/applications", { body });
    if (error || !data) {
      setSubmitting(false);
      setError(error?.message ?? "Could not create application");
      return;
    }
    // Kick off the first rollout, then land on the detail page where its live
    // status + logs stream. A failure to enqueue (e.g. no worker) is non-fatal
    // — the detail page shows the app pending with a Deploy button.
    void api
      .POST("/api/applications/{id}/rollout", {
        params: { path: { id: data.id } },
        body: { action: "deploy" },
      })
      .finally(() => navigate(`/applications/${data.id}`));
  }

  const ready = editing
    ? name.trim() !== "" && targetNamespace.trim() !== ""
    : name.trim() !== "" &&
      targetNamespace.trim() !== "" &&
      targetClusterId !== "" &&
      runnerClusterId !== "";

  if (!canEdit) return <Navigate to="/applications" replace />;

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate(editing ? `/applications/${appId}` : "/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        {editing ? "Back to application" : "Back to applications"}
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : loadError ? (
        <p className="mt-6 text-sm text-red-600">{loadError}</p>
      ) : (
        <>
          <div className="mt-3">
            <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
              Applications
            </p>
            <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
              {editing ? `Edit ${name}` : "New Helm application"}
            </h1>
            <p className="mt-1 text-sm text-neutral-600">
              {editing
                ? "Update this application's chart, values, and settings. Deploy the changes from its page with Upgrade."
                : "Deploy a Helm release to one of your clusters."}
            </p>
          </div>

          <form onSubmit={onSubmit} className="mt-6 max-w-3xl space-y-6">
            <Section>
              <Labeled label="Name">
                <input
                  className="w-full border border-neutral-300 px-3 py-2 text-sm"
                  placeholder="my-app"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  autoFocus
                  required
                />
              </Labeled>
            </Section>

            <Section
              title="Chart source"
              description="Where the Helm chart is pulled from."
            >
              {/* The chart source is fixed at registration, so the edit form
                  omits it entirely and only exposes the editable per-source
                  fields below. */}
              {!editing && (
                <Labeled label="Chart source">
                  <select
                    className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
                    value={chartSource}
                    onChange={(e) => {
                      setChartSource(e.target.value as ChartSource);
                      setConfig({});
                      setCredentialId("");
                      setInstallationId("");
                      setError(null);
                    }}
                  >
                    {CHART_SOURCES.map((s) => (
                      <option key={s.value} value={s.value}>
                        {s.label}
                      </option>
                    ))}
                  </select>
                  <p className="mt-1 text-xs text-neutral-500">
                    {selected.description}
                  </p>
                </Labeled>
              )}

              {selected.fields.map((field) => (
                <Labeled key={field.key} label={field.label} help={field.help}>
                  <input
                    type="text"
                    className="w-full border border-neutral-300 px-3 py-2 text-sm"
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
                    className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
                    <p className="mt-1 text-xs text-neutral-500">
                      No matching credentials yet. Add one under Admin › Private
                      Charts.
                    </p>
                  )}
                </Labeled>
              )}

              {chartSource === "git" && githubEnabled && (
                <Labeled
                  label="GitHub installation"
                  help="Optional. Required only if the repository is private. Connect one under Admin › GitHub."
                >
                  <select
                    className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
                    value={installationId}
                    onChange={(e) => setInstallationId(e.target.value)}
                  >
                    <option value="">None (public repository)</option>
                    {installations.map((inst) => (
                      <option key={inst.id} value={inst.id}>
                        {inst.account_login || inst.installation_id}
                      </option>
                    ))}
                  </select>
                  {installations.length === 0 && (
                    <p className="mt-1 text-xs text-neutral-500">
                      No GitHub installations yet. Connect one under Admin ›
                      GitHub.
                    </p>
                  )}
                </Labeled>
              )}
            </Section>

            <Section
              title="Destination"
              description="The cluster and namespace the release is deployed into, and the runner that performs the rollout."
            >
              {editing ? (
                <Readonly
                  label="Target cluster"
                  value={clusterName(targetClusterId)}
                />
              ) : (
                <Labeled
                  label="Target cluster"
                  help="The cluster the release is deployed into."
                >
                  <select
                    className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
              )}

              <Labeled label="Target namespace">
                <input
                  className="w-full border border-neutral-300 px-3 py-2 text-sm"
                  placeholder="default"
                  value={targetNamespace}
                  onChange={(e) => setTargetNamespace(e.target.value)}
                  required
                />
              </Labeled>

              {editing ? (
                <Readonly
                  label="Runner cluster"
                  value={clusterName(runnerClusterId)}
                />
              ) : (
                <Labeled
                  label="Runner cluster"
                  help="A job-running (Tekton-enabled) cluster the rollout runs on. For an in-cluster target, pick that same cluster."
                >
                  <select
                    className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
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
                      No job-running clusters yet. Enable job running (Tekton) on
                      a cluster first.
                    </p>
                  )}
                </Labeled>
              )}
            </Section>

            <Section
              title="Settings"
              description="Optional Helm release tuning."
            >
              <Labeled
                label="Release name"
                help="Helm release name; defaults to the application name."
              >
                <input
                  type="text"
                  className="w-full border border-neutral-300 px-3 py-2 text-sm"
                  placeholder="(defaults to name)"
                  value={releaseName}
                  onChange={(e) => setReleaseName(e.target.value)}
                />
              </Labeled>

              <Labeled
                label="Values (values.yaml)"
                help="Optional overrides passed to helm with -f."
              >
                <textarea
                  className="h-40 w-full border border-neutral-300 px-3 py-2 font-mono text-xs"
                  placeholder={"replicaCount: 2\nservice:\n  type: ClusterIP\n"}
                  value={values}
                  onChange={(e) => setValues(e.target.value)}
                />
              </Labeled>
            </Section>

            {error && <p className="text-sm text-red-600">{error}</p>}

            <div className="flex items-center justify-end gap-3 border-t border-neutral-200 pt-4">
              <button
                type="button"
                onClick={() =>
                  navigate(editing ? `/applications/${appId}` : "/applications")
                }
                className="text-sm text-neutral-500 hover:text-neutral-900"
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={!ready || submitting}
                className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
              >
                {editing
                  ? submitting
                    ? "Saving…"
                    : "Save changes"
                  : submitting
                    ? "Creating…"
                    : "Create application"}
              </button>
            </div>
          </form>
        </>
      )}
    </div>
  );
}

function Section({
  title,
  description,
  children,
}: {
  title?: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border border-neutral-200 bg-white p-5">
      {title && (
        <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
          {title}
        </h2>
      )}
      {description && (
        <p className="mt-1 text-xs text-neutral-500">{description}</p>
      )}
      <div className={title ? "mt-4 space-y-4" : "space-y-4"}>{children}</div>
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
      <label className="mb-1 block text-sm font-medium text-neutral-700">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-500">{help}</p>}
    </div>
  );
}

// Readonly shows a value that's fixed at registration (the target/runner
// cluster) as plain informational text in the edit form — no input control,
// since there's nothing to change.
function Readonly({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="mt-0.5 break-all text-sm text-neutral-700">
        {value || "—"}
      </dd>
    </div>
  );
}
