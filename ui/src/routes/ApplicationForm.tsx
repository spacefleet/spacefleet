import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { RunnerRequiredNotice } from "../components/RunnerRequiredNotice";
import { SlugInput } from "../components/SlugInput";
import { githubAppEnabled } from "../lib/appConfig";
import { useDocumentTitle } from "../lib/useDocumentTitle";

type CreateRequest = components["schemas"]["ApplicationCreateRequest"];
type UpdateRequest = components["schemas"]["ApplicationUpdateRequest"];
type ImportRequest = components["schemas"]["ApplicationImportRequest"];
type HelmRelease = components["schemas"]["HelmRelease"];
type Cluster = components["schemas"]["Cluster"];
type PushTrigger = components["schemas"]["PushTrigger"];

// ImportSeed is handed from the discovery step (ImportApplication) via router
// state: the cluster the release was found on, and the discovered release whose
// live name/namespace pre-fill the form.
export type ImportSeed = { clusterId: string; release: HelmRelease };

// ApplicationForm is the create/edit/import workflow for an application — the
// workflow owner (routes /applications/new and /applications/:appId/edit; import
// mode when an ImportSeed is passed via router state). It collects only the
// app-level fields: a name and the runner cluster. The deploy steps — and their
// per-component target cluster + namespace — are built afterwards in the
// workflow builder. The runner is fixed at registration, so it's read-only on edit.
// Only clusters that run jobs can be a runner; with none, the form explains
// what to set up instead of offering an empty dropdown.
export function ApplicationForm() {
  const { appId } = useParams();
  const editing = Boolean(appId);
  const location = useLocation();
  const importSeed =
    (location.state as { importSeed?: ImportSeed } | null)?.importSeed ?? null;
  const importing = !editing && importSeed !== null;
  const seedRelease = importSeed?.release;

  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [name, setName] = useState(seedRelease?.name ?? "");
  const [runnerClusterId, setRunnerClusterId] = useState("");
  // Run triggers (what a GitHub push or pull request starts) — edit mode only:
  // a new application has no components yet, so nothing could match.
  const [pushTrigger, setPushTrigger] = useState<PushTrigger>("");
  const [prPlans, setPrPlans] = useState(false);
  // Scheduled refresh: minutes between automatic drift checks (0 = never).
  const [driftInterval, setDriftInterval] = useState(0);

  const [clusters, setClusters] = useState<Cluster[]>([]);
  // Set once the cluster list loads successfully: only a known list gates
  // creation, so a failed load still leaves the form usable.
  const [clustersLoaded, setClustersLoaded] = useState(false);

  // In edit mode we must load the existing app before the form is meaningful.
  const [loading, setLoading] = useState(editing);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    void (async () => {
      const { data, error } = await api.GET("/api/clusters");
      setClusters(data ?? []);
      setClustersLoaded(!error);
    })();
  }, [currentOrg?.id]);

  // Load the existing app for edit mode.
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
      setName(data.name);
      setRunnerClusterId(data.runner_cluster_id);
      setPushTrigger(data.push_trigger ?? "");
      setPrPlans(data.pr_plans === true);
      setDriftInterval(data.drift_interval_minutes ?? 0);
      setLoading(false);
    })();
  }, [editing, appId, currentOrg?.id]);

  const title = editing
    ? "Manage application"
    : importing
      ? "Import application"
      : "Create application";

  useDocumentTitle(title);

  if (!canEdit) return <Navigate to="/applications" replace />;

  const clusterName = (id: string) =>
    clusters.find((c) => c.id === id)?.name ?? id;
  const runners = clusters.filter((c) => c.runs_jobs);
  const needsRunner = !editing && clustersLoaded && runners.length === 0;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);

    if (editing) {
      const body: UpdateRequest = {
        name: name.trim(),
        push_trigger: pushTrigger,
        pr_plans: prPlans,
        drift_interval_minutes: driftInterval,
      };
      const { error } = await api.PATCH("/api/applications/{id}", {
        params: { path: { id: appId! } },
        body,
      });
      setSubmitting(false);
      if (error) {
        setError(error.message ?? "Could not save the application");
        return;
      }
      navigate(`/applications/${appId}`);
      return;
    }

    const body: CreateRequest & ImportRequest = {
      name: name.trim(),
      runner_cluster_id: runnerClusterId,
    };
    if (importing) {
      const { data, error } = await api.POST("/api/applications/import", {
        body,
      });
      setSubmitting(false);
      if (error || !data) {
        setError(error?.message ?? "Could not import the application");
        return;
      }
      // Land on the workflow builder so the user builds the deploy steps next.
      navigate(`/applications/${data.id}/workflow`);
      return;
    }

    const { data, error } = await api.POST("/api/applications", { body });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not create the application");
      return;
    }
    navigate(`/applications/${data.id}/workflow`);
  }

  return (
    <div className="max-w-2xl">
      <button
        type="button"
        onClick={() => navigate(editing ? `/applications/${appId}` : "/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-400 hover:text-neutral-100"
      >
        <ArrowLeft className="h-4 w-4" />
        Back
      </button>

      <h1 className="mt-3 text-2xl font-bold tracking-tight">{title}</h1>
      <p className="mt-1 text-sm text-neutral-300">
        An application owns a deploy workflow. Set its name and runner cluster
        here; build the deploy steps — and their targets — in the workflow
        builder afterwards.
      </p>

      {needsRunner && (
        <RunnerRequiredNotice clusters={clusters} className="mt-6" />
      )}

      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : loadError ? (
        <p className="mt-6 text-sm text-red-400">{loadError}</p>
      ) : (
        <form onSubmit={(e) => void onSubmit(e)} className="mt-6 space-y-5">
          <Field label="Name">
            <SlugInput
              required
              value={name}
              onChange={setName}
              className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
              placeholder="my-app"
            />
          </Field>
          <p className="-mt-3 text-xs text-neutral-400">
            Seeds the Helm release names for this app&apos;s components.
          </p>

          <Field label="Runner cluster">
            {editing ? (
              <p className="text-sm text-neutral-300">
                {clusterName(runnerClusterId)}
                <span className="ml-2 text-xs text-neutral-500">
                  (fixed at registration)
                </span>
              </p>
            ) : (
              <select
                required
                value={runnerClusterId}
                onChange={(e) => setRunnerClusterId(e.target.value)}
                className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
              >
                <option value="">Select a cluster…</option>
                {runners.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <p className="-mt-3 text-xs text-neutral-400">
            The runner is the Tekton-enabled cluster the deploy jobs run on.
          </p>

          {editing && (
            <section className="space-y-3 border-t border-neutral-800 pt-5">
              <h2 className="text-sm font-semibold text-neutral-100">
                Triggers
              </h2>
              <p className="text-xs text-neutral-400">
                A push to a branch one of the components tracks (through its
                connected GitHub installation) starts the chosen run; a pull
                request against it can start a preview reported back as a
                check.
                {!githubAppEnabled() &&
                  " No GitHub App is configured on this deployment, so nothing will arrive until your operator sets one up."}
              </p>
              <Field label="On push">
                <select
                  value={pushTrigger}
                  onChange={(e) => setPushTrigger(e.target.value as PushTrigger)}
                  className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
                >
                  <option value="">Nothing</option>
                  <option value="preview">Start a preview</option>
                  <option value="deploy">Start a deploy</option>
                </select>
              </Field>
              <label className="flex items-center gap-2 text-sm text-neutral-300">
                <input
                  type="checkbox"
                  checked={prPlans}
                  onChange={(e) => setPrPlans(e.target.checked)}
                  className="h-3.5 w-3.5 accent-white"
                />
                Plan pull requests
              </label>
              <Field label="Scheduled refresh">
                <select
                  value={String(driftInterval)}
                  onChange={(e) => setDriftInterval(Number(e.target.value))}
                  className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm"
                >
                  <option value="0">Never</option>
                  <option value="60">Every hour</option>
                  <option value="360">Every 6 hours</option>
                  <option value="1440">Every day</option>
                  <option value="10080">Every week</option>
                </select>
              </Field>
              <p className="-mt-1 text-xs text-neutral-400">
                Refreshes the application on this schedule: a read-only check of
                every OpenTofu component for changes made outside of OpenTofu.
                Skipped while another run is in progress.
              </p>
            </section>
          )}

          {error && <p className="text-sm text-red-400">{error}</p>}

          <div className="flex items-center gap-3">
            <button
              type="submit"
              disabled={submitting || needsRunner}
              className="bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
            >
              {submitting
                ? "Saving…"
                : editing
                  ? "Save"
                  : importing
                    ? "Import"
                    : "Create"}
            </button>
            <button
              type="button"
              onClick={() =>
                navigate(editing ? `/applications/${appId}` : "/applications")
              }
              className="text-sm text-neutral-400 hover:text-neutral-100"
            >
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-sm font-medium text-neutral-300">
        {label}
      </span>
      {children}
    </label>
  );
}
