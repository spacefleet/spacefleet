import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { AppWindow, Plus } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type Application = components["schemas"]["Application"];

// Applications is the Applications › All Apps page: it lists the apps registered
// to the current organization and opens a dialog to create more. Each row links
// to the app's detail page (/applications/:id), where rollouts and live output
// live. The X-Organization-ID header is attached automatically (api/client.ts).
export function Applications() {
  const { currentOrg, currentRole } = useOrg();
  const canEdit = currentRole !== "viewer";
  const navigate = useNavigate();
  const [apps, setApps] = useState<Application[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/applications");
    if (error) setError(error.message ?? "Could not load applications");
    setApps(data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Applications
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">All Apps</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Deploy and manage Helm releases on your clusters.
          </p>
        </div>
        {canEdit && (
          <button
            type="button"
            onClick={() => navigate("/applications/new")}
            className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
          >
            <Plus className="h-4 w-4" />
            Create app
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-neutral-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : apps.length === 0 ? (
          <div className="p-10 text-center">
            <AppWindow className="mx-auto h-8 w-8 text-neutral-300" />
            <p className="mt-3 text-sm font-medium text-neutral-700">
              No applications yet
            </p>
            <p className="mt-1 text-sm text-neutral-500">
              Create your first Helm application to deploy a release.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Namespace</th>
                <th className="px-4 py-2 font-medium">Origin</th>
              </tr>
            </thead>
            <tbody>
              {apps.map((a) => (
                <tr
                  key={a.id}
                  onClick={() => navigate(`/applications/${a.id}`)}
                  className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    {a.name}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {a.target_namespace}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {a.imported ? "Imported" : "Created"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
