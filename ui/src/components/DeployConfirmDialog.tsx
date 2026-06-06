import { useCallback, useEffect, useState } from "react";
import { ArrowUpCircle, CheckCircle2, Loader2, Play, RefreshCw, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useObjectStream } from "../lib/useObjectStream";
import { DiffView } from "./DiffView";

type Application = components["schemas"]["Application"];
type ApplicationDiff = components["schemas"]["ApplicationDiff"];

// DeployConfirmDialog is the ArgoCD-style confirmation gate: before a deploy or
// upgrade actually runs, it shows the diff against the live cluster (the cached
// result of the last refresh) so the operator can see what will change and
// confirm. It can re-run the diff inline ("Refresh diff") and only fires the
// rollout on an explicit confirm.
export function DeployConfirmDialog({
  app,
  action,
  onClose,
  onConfirmed,
}: {
  app: Application;
  action: "deploy" | "upgrade";
  onClose: () => void;
  onConfirmed: (updated: Application) => void;
}) {
  const [diff, setDiff] = useState<ApplicationDiff | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [force, setForce] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadDiff = useCallback(async () => {
    const { data, error } = await api.GET("/api/applications/{id}/diff", {
      params: { path: { id: app.id } },
    });
    if (error) setError(error.message ?? "Could not load the diff");
    else setDiff(data ?? null);
    setLoading(false);
  }, [app.id]);

  useEffect(() => {
    void loadDiff();
  }, [loadDiff]);

  // While a refresh runs, stream the app row; when sync_status settles, reload
  // the cached diff.
  const { value: streamedApp } = useObjectStream<Application>(
    `/api/applications/${app.id}/stream`,
    refreshing,
  );
  useEffect(() => {
    if (!refreshing || !streamedApp) return;
    if (streamedApp.sync_status !== "refreshing") {
      setRefreshing(false);
      void loadDiff();
    }
  }, [refreshing, streamedApp, loadDiff]);

  async function refresh() {
    setError(null);
    const { error } = await api.POST("/api/applications/{id}/refresh", {
      params: { path: { id: app.id } },
    });
    if (error) {
      setError(error.message ?? "Could not start the refresh");
      return;
    }
    setRefreshing(true);
  }

  async function confirm() {
    setConfirming(true);
    setError(null);
    const { data, error } = await api.POST("/api/applications/{id}/rollout", {
      params: { path: { id: app.id } },
      body: { action, force },
    });
    setConfirming(false);
    if (error || !data) {
      setError(error?.message ?? "Could not start the rollout");
      return;
    }
    onConfirmed(data);
  }

  const verb = action === "upgrade" ? "Upgrade" : "Deploy";
  const busy = refreshing || confirming;

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-2xl border border-neutral-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            {action === "upgrade" ? (
              <ArrowUpCircle className="h-5 w-5 text-neutral-700" />
            ) : (
              <Play className="h-5 w-5 text-neutral-700" />
            )}
            {verb} {app.name}
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="text-neutral-400 hover:text-neutral-700 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm text-neutral-600">
              Review what this {verb.toLowerCase()} will change in the live
              cluster, then confirm.
            </p>
            <button
              type="button"
              onClick={() => void refresh()}
              disabled={busy}
              className="inline-flex shrink-0 items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
              Refresh diff
            </button>
          </div>

          {loading ? (
            <p className="text-sm text-neutral-500">Loading the diff…</p>
          ) : refreshing ? (
            <p className="inline-flex items-center gap-1.5 text-sm text-neutral-500">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Computing the diff against the live cluster…
            </p>
          ) : diff?.sync_status === "out_of_sync" && diff.diff ? (
            <DiffView diff={diff.diff} />
          ) : diff?.sync_status === "synced" ? (
            <p className="inline-flex items-center gap-1.5 text-sm text-green-700">
              <CheckCircle2 className="h-4 w-4" />
              In sync — deploying would change nothing.
            </p>
          ) : diff?.sync_status === "error" ? (
            <p className="text-sm text-red-600">
              The last refresh failed{diff.sync_message ? `: ${diff.sync_message}` : "."}
            </p>
          ) : (
            <p className="text-sm text-neutral-500">
              No diff yet. Use <span className="font-medium">Refresh diff</span> to
              compute what this {verb.toLowerCase()} would change.
            </p>
          )}

          {error && <p className="text-sm text-red-600">{error}</p>}
        </div>

        <label className="flex cursor-pointer items-start gap-2 border-t border-neutral-200 px-5 py-3 text-sm">
          <input
            type="checkbox"
            checked={force}
            onChange={(e) => setForce(e.target.checked)}
            disabled={busy}
            className="mt-0.5 h-4 w-4 shrink-0 accent-black disabled:opacity-50"
          />
          <span className="text-neutral-700">
            Force roll resources
            <span className="block text-xs text-neutral-500">
              Restart the release's workloads even if nothing changed, so pods
              cycle as if there were a diff.
            </span>
          </span>
        </label>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-200 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="text-sm text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void confirm()}
            disabled={busy}
            className="inline-flex items-center gap-1.5 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {confirming && <Loader2 className="h-4 w-4 animate-spin" />}
            Confirm {verb.toLowerCase()}
          </button>
        </div>
      </div>
    </div>
  );
}
