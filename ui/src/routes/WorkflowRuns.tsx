import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { formatDuration } from "../lib/duration";
import { RunStatusBadge } from "../components/workflow/status";

type WorkflowRun = components["schemas"]["WorkflowRun"];

// WorkflowRuns is the run history for an application's workflow (route
// /applications/:appId/runs): a table of deploy/uninstall/preview runs,
// newest-first, each linking to its live run view.
export function WorkflowRuns() {
  const { appId = "" } = useParams();
  const { currentOrg } = useOrg();
  const navigate = useNavigate();

  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/applications/{id}/runs", {
      params: { path: { id: appId } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not load runs");
      setLoading(false);
      return;
    }
    setRuns(data.runs);
    setLoading(false);
  }, [appId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate(`/applications/${appId}/workflow`)}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to workflow
      </button>

      <h1 className="mt-3 text-2xl font-bold tracking-tight">Run history</h1>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : runs.length === 0 ? (
        <p className="mt-6 text-sm text-neutral-500">
          No runs yet. Start one from the workflow page.
        </p>
      ) : (
        <div className="mt-6 border border-neutral-200 bg-white">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Run</th>
                <th className="px-4 py-2 font-medium">Action</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Started</th>
                <th className="px-4 py-2 font-medium">Duration</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r, i) => (
                <tr
                  key={r.id}
                  onClick={() => navigate(`/applications/${appId}/runs/${r.id}`)}
                  className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    <Link
                      to={`/applications/${appId}/runs/${r.id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="hover:underline"
                    >
                      #{runs.length - i}
                    </Link>
                  </td>
                  <td className="px-4 py-3 capitalize text-neutral-600">
                    {r.action}
                  </td>
                  <td className="px-4 py-3">
                    <RunStatusBadge status={r.status} />
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {new Date(r.created_at).toLocaleString()}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {formatDuration(r.created_at, r.finished_at ?? undefined)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
