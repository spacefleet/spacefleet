import { useEffect, useState } from "react";
import { Navigate, useNavigate } from "react-router";
import { ArrowLeft, PackageSearch } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import type { ImportSeed } from "./ApplicationForm";

type Cluster = components["schemas"]["Cluster"];
type HelmRelease = components["schemas"]["HelmRelease"];

// ImportApplication is the discovery step of importing an existing Helm release:
// pick a cluster (and optionally a namespace), list the releases already running
// on it, and hand a chosen release off to ApplicationForm in import mode (via
// router state) to adopt it as an application. The adopt creates the application
// (its name + clusters pre-filled from the release); the user then builds the
// deploy workflow from components on the canvas.
export function ImportApplication() {
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [clusterId, setClusterId] = useState("");
  const [namespace, setNamespace] = useState("");

  const [releases, setReleases] = useState<HelmRelease[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let ignore = false;
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      // Ignore a response that lands after the org switched out from under us,
      // so a stale cluster list (and any releases discovered from it) can't
      // render against the new org.
      if (ignore) return;
      setClusters(data ?? []);
      // A new org means the previously discovered releases belong to a cluster
      // that may not exist here — clear them so nothing stale is shown.
      setReleases(null);
      setClusterId("");
    })();
    return () => {
      ignore = true;
    };
  }, [currentOrg?.id]);

  async function discover(e: React.FormEvent) {
    e.preventDefault();
    if (clusterId === "") return;
    setLoading(true);
    setError(null);
    setReleases(null);
    const { data, error } = await api.GET("/api/clusters/{id}/releases", {
      params: {
        path: { id: clusterId },
        query: namespace.trim() ? { namespace: namespace.trim() } : {},
      },
    });
    setLoading(false);
    if (error) {
      setError(error.message ?? "Could not list releases on this cluster");
      return;
    }
    // Newest deployments first, so the most recently touched releases are on top.
    const list = [...(data ?? [])].sort((a, b) =>
      (b.updated_at ?? "").localeCompare(a.updated_at ?? ""),
    );
    setReleases(list);
  }

  function importRelease(release: HelmRelease) {
    const seed: ImportSeed = { clusterId, release };
    navigate("/applications/new", { state: { importSeed: seed } });
  }

  if (!canEdit) return <Navigate to="/applications" replace />;

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to applications
      </button>

      <div className="mt-3">
        <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
          Applications
        </p>
        <h1 className="mt-1 text-2xl font-bold tracking-tight">
          Import existing release
        </h1>
        <p className="mt-1 text-sm text-neutral-600">
          Find a Helm release already running on one of your clusters and adopt
          it as a managed application. You then build its deploy workflow on the
          canvas. Nothing is redeployed.
        </p>
      </div>

      <form
        onSubmit={discover}
        className="mt-6 max-w-3xl border border-neutral-200 bg-white p-4"
      >
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex-1">
            <span className="mb-1 block text-sm font-medium text-neutral-700">
              Cluster
            </span>
            <select
              className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
              value={clusterId}
              onChange={(e) => setClusterId(e.target.value)}
              required
            >
              <option value="">Select a cluster…</option>
              {clusters.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label className="flex-1">
            <span className="mb-1 block text-sm font-medium text-neutral-700">
              Namespace <span className="text-neutral-400">(optional)</span>
            </span>
            <input
              type="text"
              className="w-full border border-neutral-300 px-3 py-2 text-sm"
              placeholder="all namespaces"
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
            />
          </label>
          <button
            type="submit"
            disabled={clusterId === "" || loading}
            className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {loading ? "Discovering…" : "Discover"}
          </button>
        </div>
      </form>

      {error && <p className="mt-4 text-sm text-red-600">{error}</p>}

      {releases !== null && (
        <div className="mt-6 max-w-3xl border border-neutral-200 bg-white">
          {releases.length === 0 ? (
            <div className="p-10 text-center">
              <PackageSearch className="mx-auto h-8 w-8 text-neutral-300" />
              <p className="mt-3 text-sm font-medium text-neutral-700">
                No Helm releases found
              </p>
              <p className="mt-1 text-sm text-neutral-500">
                Nothing to import on this cluster
                {namespace.trim() ? ` in namespace ${namespace.trim()}` : ""}.
              </p>
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                  <th className="px-4 py-2 font-medium">Release</th>
                  <th className="px-4 py-2 font-medium">Namespace</th>
                  <th className="px-4 py-2 font-medium">Chart</th>
                  <th className="px-4 py-2 font-medium">Status</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {releases.map((r) => (
                  <tr
                    key={`${r.namespace}/${r.name}`}
                    className="border-b border-neutral-100 last:border-0"
                  >
                    <td className="px-4 py-3 font-medium text-neutral-900">
                      {r.name}
                    </td>
                    <td className="px-4 py-3 text-neutral-600">
                      {r.namespace}
                    </td>
                    <td className="px-4 py-3 text-neutral-600">
                      {r.chart_name}
                      {r.chart_version ? `:${r.chart_version}` : ""}
                    </td>
                    <td className="px-4 py-3 text-neutral-600">{r.status}</td>
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => importRelease(r)}
                        className="border border-neutral-300 px-3 py-1.5 text-sm font-medium text-neutral-800 hover:bg-neutral-50"
                      >
                        Import
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}
