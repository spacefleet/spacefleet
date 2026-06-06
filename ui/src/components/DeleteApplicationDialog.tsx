import { useState } from "react";
import { AlertTriangle, Loader2, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";

type Application = components["schemas"]["Application"];

// DeleteApplicationDialog is the destructive action for an application: it
// permanently removes the application (and its workflow components and run
// history, which cascade) from Spacefleet. It does not touch any release the
// workflow may have deployed — to remove that, run an "uninstall" workflow first.
export function DeleteApplicationDialog({
  app,
  onClose,
  onDeleted,
}: {
  app: Application;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function runDelete() {
    setDeleting(true);
    setError(null);
    const { error } = await api.DELETE("/api/applications/{id}", {
      params: { path: { id: app.id } },
    });
    if (error) {
      setError(error.message ?? "Could not delete this application");
      setDeleting(false);
      return;
    }
    onDeleted();
  }

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
            disabled={deleting}
            className="text-neutral-400 hover:text-neutral-700 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <p className="text-sm text-neutral-600">
            This permanently removes the application — along with its deploy
            workflow and run history — from Spacefleet. This cannot be undone.
          </p>
          <p className="text-sm text-neutral-500">
            Any release the workflow deployed is left running on the cluster. To
            remove it, run an uninstall workflow before deleting.
          </p>
          {error && <p className="text-sm text-red-600">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-200 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={deleting}
            className="text-sm text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void runDelete()}
            disabled={deleting}
            className="inline-flex items-center gap-1.5 bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
          >
            {deleting && <Loader2 className="h-4 w-4 animate-spin" />}
            {deleting ? "Deleting…" : "Delete"}
          </button>
        </div>
      </div>
    </div>
  );
}
