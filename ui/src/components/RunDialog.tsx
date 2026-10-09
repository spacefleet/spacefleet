import { useState } from "react";
import { Loader2, Play, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { runStartError } from "./workflow/runAction";

type Application = components["schemas"]["Application"];

// RunDialog starts a deploy of the application's saved workflow, with the
// per-run options gathered first (today: forcing a workload roll). It is the
// one place run options live, so a new option is a new field here rather than
// another control on the application page. On success it hands the new run's
// id back so the caller can open its live view.
export function RunDialog({
  app,
  onClose,
  onStarted,
}: {
  app: Application;
  onClose: () => void;
  onStarted: (runId: string) => void;
}) {
  const [forceRoll, setForceRoll] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function start() {
    setStarting(true);
    setError(null);
    const { data, error, response } = await api.POST(
      "/api/applications/{id}/runs",
      {
        params: { path: { id: app.id } },
        body: { action: "deploy", force: forceRoll },
      },
    );
    if (error || !data) {
      setError(runStartError(error, response?.status));
      setStarting(false);
      return;
    }
    onStarted(data.id);
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="run-dialog-title"
        className="mt-12 w-full max-w-lg border border-neutral-200 bg-white shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
          <h2
            id="run-dialog-title"
            className="text-lg font-semibold tracking-tight"
          >
            Run {app.name}
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={starting}
            className="text-neutral-400 hover:text-neutral-700 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <p className="text-sm text-neutral-600">
            Deploys the saved workflow: each stage runs in order, the components
            in a stage run in parallel, and any approval gates still apply.
          </p>
          <div>
            <h3 className="text-xs font-medium uppercase tracking-wide text-neutral-400">
              Options
            </h3>
            <label className="mt-2 flex items-start gap-2 text-sm text-neutral-700">
              <input
                type="checkbox"
                checked={forceRoll}
                onChange={(e) => setForceRoll(e.target.checked)}
                disabled={starting}
                className="mt-0.5 h-3.5 w-3.5 accent-black"
              />
              <span>
                Force workload roll
                <span className="block text-xs text-neutral-500">
                  Restart the Helm components&apos; workloads even when their
                  rendered manifests are unchanged.
                </span>
              </span>
            </label>
          </div>
          {error && <p className="text-sm text-red-600">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-200 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={starting}
            className="text-sm text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void start()}
            disabled={starting}
            className="inline-flex items-center gap-1.5 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {starting ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Play className="h-4 w-4" />
            )}
            {starting ? "Starting…" : "Run"}
          </button>
        </div>
      </div>
    </div>
  );
}
