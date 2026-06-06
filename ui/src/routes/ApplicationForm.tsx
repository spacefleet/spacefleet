import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { githubAppEnabled } from "../lib/appConfig";
import { CHART_SOURCES } from "../components/chartSources";

type Application = components["schemas"]["Application"];
type ChartSource = components["schemas"]["ChartSource"];
type ValuesSource = components["schemas"]["ValuesSource"];
type CreateRequest = components["schemas"]["ApplicationCreateRequest"];
type UpdateRequest = components["schemas"]["ApplicationUpdateRequest"];
type ImportRequest = components["schemas"]["ApplicationImportRequest"];
type HelmRelease = components["schemas"]["HelmRelease"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];

// ImportSeed is handed from the discovery step (ImportApplication) via router
// state: the cluster the release was found on, and the discovered release whose
// live state (name, namespace, current values) pre-fills the form.
export type ImportSeed = { clusterId: string; release: HelmRelease };

// seedConfig pre-fills the chart coordinates we can infer from a discovered
// release — its chart name and version. The operator still chooses the chart
// *source* (where to pull it from); these keys are the ones http_repo/oci/git
// read (chart, version), ignored by sources that don't use them.
function seedConfig(release: HelmRelease): Record<string, string> {
  const cfg: Record<string, string> = {};
  if (release.chart_name) cfg.chart = release.chart_name;
  if (release.chart_version) cfg.version = release.chart_version;
  return cfg;
}

// Which chart sources take a username/password chart credential. git charts use
// a connected GitHub App instead, so they take no chart credential.
function sourceSupportsCredential(source: ChartSource): boolean {
  return source === "http_repo" || source === "oci";
}

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
  const location = useLocation();
  // The discovery step (ImportApplication) hands a release seed via router state.
  // Import mode pre-fills the form from the live release and adopts it without a
  // rollout; it's distinct from create (which deploys) and edit (which loads).
  const importSeed =
    (location.state as { importSeed?: ImportSeed } | null)?.importSeed ?? null;
  const importing = !editing && importSeed !== null;
  const seedRelease = importSeed?.release;

  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [name, setName] = useState(seedRelease?.name ?? "");
  const [chartSource, setChartSource] = useState<ChartSource>("http_repo");
  const [config, setConfig] = useState<Record<string, string>>(
    seedRelease ? seedConfig(seedRelease) : {},
  );
  const [values, setValues] = useState(seedRelease?.values ?? "");
  const [releaseName, setReleaseName] = useState(seedRelease?.name ?? "");
  const [targetNamespace, setTargetNamespace] = useState(
    seedRelease?.namespace ?? "",
  );
  const [targetClusterId, setTargetClusterId] = useState(
    importSeed?.clusterId ?? "",
  );
  const [runnerClusterId, setRunnerClusterId] = useState("");
  const [credentialId, setCredentialId] = useState("");
  const [installationId, setInstallationId] = useState("");
  // Optional values-from-git sources (orthogonal to the chart source): an ordered
  // list of repos to pull values files from, layered under the inline values
  // below — earlier first, inline wins.
  const [valuesSources, setValuesSources] = useState<ValuesSource[]>([]);

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
    setValuesSources(app.values_sources ?? []);
  }

  const selected = CHART_SOURCES.find((s) => s.value === chartSource)!;
  // Only job-running clusters can host a rollout's TaskRun.
  const runnerClusters = clusters.filter((c) => c.runs_jobs);
  // Whether the selected source takes a chart credential (none for git).
  const credentialApplies = sourceSupportsCredential(chartSource);
  const clusterName = (id: string) =>
    clusters.find((c) => c.id === id)?.name ?? id;
  // Values-from-git is in play once any source has a repo URL. The GitHub
  // installation authenticates a private github.com clone of either the chart
  // (git source) or a values repo, so it's offered whenever either is git.
  const valuesFromGit = valuesSources.some((s) => s.repo_url.trim() !== "");
  const installationApplies =
    githubEnabled && (chartSource === "git" || valuesFromGit);

  function setField(key: string, value: string) {
    setConfig((c) => ({ ...c, [key]: value }));
  }

  function updateSource(i: number, key: keyof ValuesSource, value: string) {
    setValuesSources((arr) =>
      arr.map((s, j) => (j === i ? { ...s, [key]: value } : s)),
    );
  }
  function addSource() {
    setValuesSources((arr) => [...arr, { repo_url: "", path: "" }]);
  }
  function removeSource(i: number) {
    setValuesSources((arr) => arr.filter((_, j) => j !== i));
  }

  // The values sources to send: trimmed, repo-less rows dropped, empty ref omitted.
  function cleanValuesSources(): ValuesSource[] {
    return valuesSources
      .filter((s) => s.repo_url.trim() !== "")
      .map((s) => ({
        repo_url: s.repo_url.trim(),
        path: (s.path ?? "").trim(),
        ...(s.git_ref?.trim() ? { git_ref: s.git_ref.trim() } : {}),
      }));
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
    const sources = cleanValuesSources();

    if (editing) {
      const body: UpdateRequest = {
        name: name.trim(),
        config: cfg,
        values,
        // Sent unconditionally: the form reflects the desired set, so an empty
        // array clears any previously configured sources.
        values_sources: sources,
        release_name: releaseName.trim(),
        target_namespace: targetNamespace.trim(),
        // A cleared selector detaches via the nil UUID; an absent selector
        // (wrong source type / GitHub disabled / not pulling from git) leaves the
        // field untouched.
        chart_credential_id: credentialApplies
          ? credentialId || NIL_UUID
          : undefined,
        github_installation_id: installationApplies
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

    // Import (adopt): the release is already deployed, so this POSTs to /import
    // (no rollout) and lands on the detail page, where the auto-refresh's diff
    // shows whether the configured source reproduces the live release.
    if (importing) {
      const body: ImportRequest = {
        name: name.trim(),
        chart_source: chartSource,
        config: cfg,
        target_namespace: targetNamespace.trim(),
        target_cluster_id: targetClusterId,
        runner_cluster_id: runnerClusterId,
      };
      if (values.trim() !== "") body.values = values;
      if (sources.length > 0) body.values_sources = sources;
      if (releaseName.trim() !== "") body.release_name = releaseName.trim();
      if (credentialId !== "") body.chart_credential_id = credentialId;
      if (installationApplies && installationId !== "")
        body.github_installation_id = installationId;

      const { data, error } = await api.POST("/api/applications/import", {
        body,
      });
      setSubmitting(false);
      if (error || !data) {
        setError(error?.message ?? "Could not import release");
        return;
      }
      navigate(`/applications/${data.id}`);
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
    if (sources.length > 0) body.values_sources = sources;
    if (releaseName.trim() !== "") body.release_name = releaseName.trim();
    if (credentialId !== "") body.chart_credential_id = credentialId;
    if (installationApplies && installationId !== "")
      body.github_installation_id = installationId;

    const { data, error } = await api.POST("/api/applications", { body });
    if (error || !data) {
      setSubmitting(false);
      setError(error?.message ?? "Could not create application");
      return;
    }
    // The application exists now; the rollout enqueue below is fire-and-forget
    // (a failure is non-fatal — the detail page shows it pending with a Deploy
    // button). Clear the submitting state here so the button doesn't stay stuck
    // on "Creating…" during the brief window before the navigate lands.
    setSubmitting(false);
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

  const backTarget = editing
    ? `/applications/${appId}`
    : importing
      ? "/applications/import"
      : "/applications";

  if (!canEdit) return <Navigate to="/applications" replace />;

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate(backTarget)}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        {editing
          ? "Back to application"
          : importing
            ? "Back to discovery"
            : "Back to applications"}
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
              {editing
                ? `Edit ${name}`
                : importing
                  ? "Import Helm release"
                  : "New Helm application"}
            </h1>
            <p className="mt-1 text-sm text-neutral-600">
              {editing
                ? "Update this application's chart, values, and settings. Deploy the changes from its page with Upgrade."
                : importing
                  ? "Adopt this existing release. Confirm where its chart comes from and pick a runner; its current values are pre-filled below. Nothing is redeployed."
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

              {credentialApplies && (
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
                    {credentials.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                  {credentials.length === 0 && (
                    <p className="mt-1 text-xs text-neutral-500">
                      No credentials yet. Add one under Admin › Private Charts.
                    </p>
                  )}
                </Labeled>
              )}

              {chartSource === "git" && githubEnabled && (
                <InstallationSelect
                  value={installationId}
                  onChange={setInstallationId}
                  installations={installations}
                  help="Optional. Required only if the chart repository is private. Connect one under Admin › GitHub."
                />
              )}
            </Section>

            <Section
              title="Values from Git"
              description="Optionally pull values files from one or more Git repositories. They're applied in order — top to bottom — then the inline values below, which wins."
            >
              {valuesSources.length === 0 ? (
                <p className="text-sm text-neutral-500">
                  No git values sources. The inline values below are used on
                  their own.
                </p>
              ) : (
                <ol className="space-y-3">
                  {valuesSources.map((src, i) => (
                    <li
                      key={i}
                      className="border border-neutral-200 bg-neutral-50 p-3"
                    >
                      <div className="mb-2 flex items-center justify-between">
                        <span className="text-xs font-medium text-neutral-500">
                          Source {i + 1}
                        </span>
                        <button
                          type="button"
                          onClick={() => removeSource(i)}
                          className="text-xs text-neutral-500 hover:text-red-600"
                        >
                          Remove
                        </button>
                      </div>
                      <div className="space-y-3">
                        <input
                          type="text"
                          aria-label={`Source ${i + 1} repository URL`}
                          className="w-full border border-neutral-300 px-3 py-2 text-sm"
                          placeholder="https://github.com/org/config.git"
                          value={src.repo_url}
                          onChange={(e) =>
                            updateSource(i, "repo_url", e.target.value)
                          }
                          required
                        />
                        <div className="flex gap-3">
                          <input
                            type="text"
                            aria-label={`Source ${i + 1} branch or tag`}
                            className="w-1/3 border border-neutral-300 px-3 py-2 text-sm"
                            placeholder="branch/tag (optional)"
                            value={src.git_ref ?? ""}
                            onChange={(e) =>
                              updateSource(i, "git_ref", e.target.value)
                            }
                          />
                          <input
                            type="text"
                            aria-label={`Source ${i + 1} values file path`}
                            className="flex-1 border border-neutral-300 px-3 py-2 text-sm"
                            placeholder="envs/prod/values.yaml"
                            value={src.path}
                            onChange={(e) =>
                              updateSource(i, "path", e.target.value)
                            }
                            required
                          />
                        </div>
                      </div>
                    </li>
                  ))}
                </ol>
              )}

              <button
                type="button"
                onClick={addSource}
                className="text-sm font-medium text-neutral-700 hover:text-black"
              >
                + Add values source
              </button>

              {/* When the chart itself is git, the installation selector in the
                  Chart source section already covers github.com auth (one
                  installation serves both chart and values). */}
              {chartSource !== "git" && githubEnabled && valuesFromGit && (
                <InstallationSelect
                  value={installationId}
                  onChange={setInstallationId}
                  installations={installations}
                  help="Optional. Required only if a values repository is private. Connect one under Admin › GitHub."
                />
              )}
            </Section>

            <Section
              title="Destination"
              description="The cluster and namespace the release is deployed into, and the runner that performs the rollout."
            >
              {/* The target cluster is fixed when editing, and for import it's the
                  cluster the release was discovered on. */}
              {editing || importing ? (
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

              {/* The namespace describes where the live release is installed, so
                  import locks it (changing it would target a different release). */}
              {importing ? (
                <Readonly label="Target namespace" value={targetNamespace} />
              ) : (
                <Labeled label="Target namespace">
                  <input
                    className="w-full border border-neutral-300 px-3 py-2 text-sm"
                    placeholder="default"
                    value={targetNamespace}
                    onChange={(e) => setTargetNamespace(e.target.value)}
                    required
                  />
                </Labeled>
              )}

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
              {/* The release name identifies the live release, so import locks it
                  to what was discovered. */}
              {importing ? (
                <Readonly label="Release name" value={releaseName} />
              ) : (
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
              )}

              <Labeled
                label="Values (values.yaml)"
                help="Optional overrides passed to helm with -f."
              >
                {importing && (
                  <p className="mb-2 border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
                    These are the release's current values, pulled live from the
                    cluster. They may contain secrets passed at install time —
                    review before importing, as they're stored with the
                    application.
                  </p>
                )}
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
                onClick={() => navigate(backTarget)}
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
                  : importing
                    ? submitting
                      ? "Importing…"
                      : "Import release"
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

// InstallationSelect is the GitHub App installation picker, shared by the chart
// (git source) and the values-from-git source — one installation authenticates
// any private github.com clone in the rollout, so both bind the same value.
function InstallationSelect({
  value,
  onChange,
  installations,
  help,
}: {
  value: string;
  onChange: (v: string) => void;
  installations: GitHubInstallation[];
  help: string;
}) {
  return (
    <Labeled label="GitHub installation" help={help}>
      <select
        className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
        value={value}
        onChange={(e) => onChange(e.target.value)}
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
          No GitHub installations yet. Connect one under Admin › GitHub.
        </p>
      )}
    </Labeled>
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
