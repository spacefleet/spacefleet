import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type CreateRequest = components["schemas"]["ApplicationCreateRequest"];
type UpdateRequest = components["schemas"]["ApplicationUpdateRequest"];
type ImportRequest = components["schemas"]["ApplicationImportRequest"];
type HelmRelease = components["schemas"]["HelmRelease"];
type Cluster = components["schemas"]["Cluster"];

// ImportSeed is handed from the discovery step (ImportApplication) via router
// state: the cluster the release was found on, and the discovered release whose
// live name/namespace pre-fill the form.
export type ImportSeed = { clusterId: string; release: HelmRelease };

// ApplicationForm is the create/edit/import workflow for an application — the
// workflow owner (routes /applications/new and /applications/:appId/edit; import
// mode when an ImportSeed is passed via router state). It collects only the
// app-level fields: a name and the runner cluster. The deploy steps — and their
// per-component target cluster + namespace — are built afterwards on the workflow
// canvas. The runner is fixed at registration, so it's read-only on edit.
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

  const [clusters, setClusters] = useState<Cluster[]>([]);

  // In edit mode we must load the existing app before the form is meaningful.
  const [loading, setLoading] = useState(editing);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      setClusters(data ?? []);
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
      setLoading(false);
    })();
  }, [editing, appId, currentOrg?.id]);

  if (!canEdit) return <Navigate to="/applications" replace />;

  const clusterName = (id: string) =>
    clusters.find((c) => c.id === id)?.name ?? id;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);

    if (editing) {
      const body: UpdateRequest = {
        name: name.trim(),
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
      // Land on the workflow canvas so the user builds the deploy steps next.
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

  const title = editing
    ? "Edit application"
    : importing
      ? "Import application"
      : "Create application";

  return (
    <div className="max-w-2xl">
      <button
        type="button"
        onClick={() => navigate(editing ? `/applications/${appId}` : "/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back
      </button>

      <h1 className="mt-3 text-2xl font-bold tracking-tight">{title}</h1>
      <p className="mt-1 text-sm text-neutral-600">
        An application owns a deploy workflow. Set its name and runner cluster
        here; build the deploy steps — and their targets — on the workflow canvas
        afterwards.
      </p>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : loadError ? (
        <p className="mt-6 text-sm text-red-600">{loadError}</p>
      ) : (
        <form onSubmit={(e) => void onSubmit(e)} className="mt-6 space-y-5">
          <Field label="Name">
            <input
              type="text"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full border border-neutral-300 px-3 py-2 text-sm"
              placeholder="my-app"
              pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
              maxLength={63}
              title={'A slug: lowercase letters, digits, and hyphens (e.g. "my-app")'}
            />
          </Field>
          <p className="-mt-3 text-xs text-neutral-500">
            A slug — lowercase letters, digits, and hyphens. It seeds the Helm
            release names for this app&apos;s components.
          </p>

          <Field label="Runner cluster">
            {editing ? (
              <p className="text-sm text-neutral-700">
                {clusterName(runnerClusterId)}
                <span className="ml-2 text-xs text-neutral-400">
                  (fixed at registration)
                </span>
              </p>
            ) : (
              <select
                required
                value={runnerClusterId}
                onChange={(e) => setRunnerClusterId(e.target.value)}
                className="w-full border border-neutral-300 px-3 py-2 text-sm"
              >
                <option value="">Select a cluster…</option>
                {clusters.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <p className="-mt-3 text-xs text-neutral-500">
            The runner is the Tekton-enabled cluster the deploy jobs run on.
          </p>

          {error && <p className="text-sm text-red-600">{error}</p>}

          <div className="flex items-center gap-3">
            <button
              type="submit"
              disabled={submitting}
              className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
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
              className="text-sm text-neutral-500 hover:text-neutral-900"
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
      <span className="mb-1 block text-sm font-medium text-neutral-700">
        {label}
      </span>
      {children}
    </label>
  );
}
