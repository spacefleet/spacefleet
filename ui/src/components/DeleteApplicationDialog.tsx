import { useEffect, useRef, useState } from "react";
import { AlertTriangle, Loader2, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useObjectStream } from "../lib/useObjectStream";
import { usePodLogs } from "../lib/usePodLogs";

type Application = components["schemas"]["Application"];

// Phases of the delete flow. "confirm" shows the options; "uninstalling" runs the
// helm uninstall job and streams its logs; "deleting" removes the record; "failed"
// surfaces an uninstall/delete error with an escape hatch to delete anyway.
type Phase = "confirm" | "uninstalling" | "deleting" | "failed";

// DeleteApplicationDialog is the single destructive action for an application. It
// offers to uninstall the Helm release first (checked by default when a release
// may be live): it runs the uninstall job, streams its logs here, and only on a
// successful uninstall deletes the app — a failed uninstall aborts the delete.
// Unchecking it (or a "Delete anyway" after a failure) drops just the record and
// leaves the release for the user to handle.
export function DeleteApplicationDialog({
  app,
  onClose,
  onDeleted,
}: {
  app: Application;
  onClose: () => void;
  onDeleted: () => void;
}) {
  // A release may exist on the cluster only once a rollout has run; pending or
  // already-uninstalled apps have nothing to uninstall.
  const hasRelease = app.status === "deployed" || app.status === "failed";

  const [phase, setPhase] = useState<Phase>("confirm");
  const [uninstallFirst, setUninstallFirst] = useState(hasRelease);
  const [error, setError] = useState<string | null>(null);
  // The run name to follow logs for, learned from the uninstall response/stream.
  const [runName, setRunName] = useState(app.last_run_name ?? "");

  const uninstalling = phase === "uninstalling";

  // Watch the app to a terminal status while the uninstall job runs.
  const { value: streamedApp } = useObjectStream<Application>(
    `/api/applications/${app.id}/stream`,
    uninstalling,
  );
  // Follow the uninstall job's helm logs once its pod (run) exists.
  const { lines } = usePodLogs(
    `/api/applications/${app.id}/logs?run=${encodeURIComponent(runName)}`,
    uninstalling && runName !== "",
  );

  useEffect(() => {
    if (streamedApp?.last_run_name) setRunName(streamedApp.last_run_name);
  }, [streamedApp?.last_run_name]);

  // React to the uninstall settling: success → delete the record; failure →
  // abort and show why. Guarded on `uninstalling` so it fires once per run.
  useEffect(() => {
    if (!uninstalling || !streamedApp) return;
    if (streamedApp.status === "uninstalled") {
      void runDelete();
    } else if (streamedApp.status === "failed") {
      setError(streamedApp.status_message || "The uninstall failed.");
      setPhase("failed");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [uninstalling, streamedApp?.status]);

  async function runDelete() {
    setPhase("deleting");
    setError(null);
    const { error } = await api.DELETE("/api/applications/{id}", {
      params: { path: { id: app.id } },
    });
    if (error) {
      setError(error.message ?? "Could not delete this application");
      setPhase("failed");
      return;
    }
    onDeleted();
  }

  async function onConfirm() {
    if (!uninstallFirst) {
      await runDelete();
      return;
    }
    setError(null);
    setPhase("uninstalling");
    const { data, error } = await api.POST("/api/applications/{id}/uninstall", {
      params: { path: { id: app.id } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not start the uninstall");
      setPhase("failed");
      return;
    }
    if (data.last_run_name) setRunName(data.last_run_name);
  }

  const busy = phase === "uninstalling" || phase === "deleting";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-lg border border-neutral-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <AlertTriangle className="h-5 w-5 text-red-600" />
            Delete {app.name}
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
          <p className="text-sm text-neutral-600">
            This permanently removes the application from Spacefleet. This cannot
            be undone.
          </p>

          {hasRelease && (
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-0.5"
                checked={uninstallFirst}
                disabled={phase !== "confirm"}
                onChange={(e) => setUninstallFirst(e.target.checked)}
              />
              <span>
                <span className="font-medium text-neutral-800">
                  Uninstall the Helm release first
                </span>
                <span className="mt-0.5 block text-xs text-neutral-500">
                  Runs <code>helm uninstall</code> on the cluster, then deletes
                  the app once it succeeds. If the uninstall fails, the app is
                  not deleted. Leave this off to delete the record and handle the
                  release yourself.
                </span>
              </span>
            </label>
          )}

          {(phase === "uninstalling" || phase === "deleting") && (
            <LogPanel
              lines={lines}
              waiting={runName === "" || phase === "deleting"}
              label={
                phase === "deleting"
                  ? "Release uninstalled — deleting the application…"
                  : "Uninstalling the Helm release…"
              }
            />
          )}

          {error && <p className="text-sm text-red-600">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-200 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="text-sm text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
          >
            Cancel
          </button>
          {phase === "failed" ? (
            // The uninstall (or a prior delete) failed; let the user drop the
            // record anyway, leaving any release for them to clean up.
            <button
              type="button"
              onClick={() => void runDelete()}
              className="bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700"
            >
              Delete anyway
            </button>
          ) : (
            <button
              type="button"
              onClick={() => void onConfirm()}
              disabled={busy}
              className="inline-flex items-center gap-1.5 bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
            >
              {busy && <Loader2 className="h-4 w-4 animate-spin" />}
              {phase === "uninstalling"
                ? "Uninstalling…"
                : phase === "deleting"
                  ? "Deleting…"
                  : "Delete"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// LogPanel renders the live helm output, auto-scrolling to the tail as lines
// arrive (mirrors the deployment log viewer's look).
function LogPanel({
  lines,
  waiting,
  label,
}: {
  lines: string[];
  waiting: boolean;
  label: string;
}) {
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines]);

  return (
    <div>
      <p className="mb-1 inline-flex items-center gap-1.5 text-xs text-neutral-500">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        {label}
      </p>
      <pre
        ref={ref}
        className="max-h-72 overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100"
      >
        {lines.length > 0
          ? lines.join("\n")
          : waiting
            ? "Waiting for log output…"
            : ""}
      </pre>
    </div>
  );
}
