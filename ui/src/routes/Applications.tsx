import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
  AppWindow,
  CheckCircle2,
  ChevronDown,
  CircleDashed,
  Loader2,
  Plus,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { CreateApplicationDialog } from "../components/CreateApplicationDialog";
import { chartSourceLabel } from "../components/chartSources";

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
  const [creating, setCreating] = useState(false);

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
        {canEdit && <CreateAppButton onHelm={() => setCreating(true)} />}
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
                <th className="px-4 py-2 font-medium">Source</th>
                <th className="px-4 py-2 font-medium">Namespace</th>
                <th className="px-4 py-2 font-medium">Status</th>
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
                    {chartSourceLabel(a.chart_source)}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {a.target_namespace}
                  </td>
                  <td className="px-4 py-3">
                    <AppStatusBadge status={a.status} message={a.status_message} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {creating && (
        <CreateApplicationDialog
          onClose={() => setCreating(false)}
          onCreated={(app) => {
            setCreating(false);
            // Kick off the first rollout, then land on the detail page where its
            // live status + logs stream. A failure to enqueue (e.g. no worker)
            // is non-fatal — the detail page shows the app pending with a Deploy
            // button.
            void api
              .POST("/api/applications/{id}/rollout", {
                params: { path: { id: app.id } },
                body: { action: "deploy" },
              })
              .finally(() => navigate(`/applications/${app.id}`));
          }}
        />
      )}
    </div>
  );
}

// CreateAppButton is a split dropdown: the primary action creates a Helm app;
// the menu lists other (not-yet-available) application types so the surface is
// ready to grow.
function CreateAppButton({ onHelm }: { onHelm: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, []);

  return (
    <div ref={ref} className="relative inline-flex">
      <button
        type="button"
        onClick={onHelm}
        className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
      >
        <Plus className="h-4 w-4" />
        Create app
      </button>
      <button
        type="button"
        aria-label="More application types"
        onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center border-l border-neutral-700 bg-black px-2 text-white hover:bg-neutral-800"
      >
        <ChevronDown className="h-4 w-4" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-10 mt-1 w-56 border border-neutral-200 bg-white py-1 shadow-lg">
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              onHelm();
            }}
            className="block w-full px-3 py-2 text-left text-sm hover:bg-neutral-50"
          >
            Helm release
          </button>
          <span className="block w-full cursor-not-allowed px-3 py-2 text-left text-sm text-neutral-400">
            Container image (coming soon)
          </span>
        </div>
      )}
    </div>
  );
}

export function AppStatusBadge({
  status,
  message,
}: {
  status: Application["status"];
  message?: string;
}) {
  const styles: Record<Application["status"], string> = {
    pending: "bg-neutral-100 text-neutral-700",
    deploying: "bg-blue-100 text-blue-800",
    deployed: "bg-green-100 text-green-800",
    failed: "bg-red-100 text-red-800",
    uninstalling: "bg-blue-100 text-blue-800",
    uninstalled: "bg-neutral-100 text-neutral-700",
  };
  const Icon: Record<Application["status"], typeof CheckCircle2> = {
    pending: CircleDashed,
    deploying: Loader2,
    deployed: CheckCircle2,
    failed: XCircle,
    uninstalling: Loader2,
    uninstalled: CircleDashed,
  };
  const I = Icon[status];
  const spinning = status === "deploying" || status === "uninstalling";
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[status]}`}
      title={status === "failed" ? message : undefined}
    >
      <I className={`h-3.5 w-3.5 ${spinning ? "animate-spin" : ""}`} />
      {status}
    </span>
  );
}
