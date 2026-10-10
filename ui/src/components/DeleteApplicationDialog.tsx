import { useEffect, useState } from "react";
import { Link } from "react-router";
import { AlertTriangle, Loader2, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { runStartError } from "./workflow/runAction";

type Application = Pick<components["schemas"]["Application"], "id" | "name">;

// What happens to everything the application deployed when it is deleted.
type Deployed = "uninstall" | "keep";

// DeleteApplicationDialog is the destructive action for an application: it
// permanently removes the application (and its workflow components and run
// history, which cascade) from Spacefleet. Deleting never touches what the
// workflow deployed, so the dialog first asks what to do about that:
//
//   - "uninstall": start an uninstall run instead of deleting. An uninstall is
//     an ordinary run (it can take minutes, and an OpenTofu destroy waits for
//     approval), so the dialog only starts it and hands the run id back; the
//     run view offers the delete once the uninstall succeeds.
//   - "keep": delete now and leave everything deployed as it is.
//
// Neither is preselected — one destroys infrastructure, the other orphans it.
// With afterUninstall set (opened from a finished uninstall run) the question
// is skipped: there's nothing left to uninstall.
export function DeleteApplicationDialog({
  app,
  afterUninstall = false,
  onClose,
  onDeleted,
  onUninstallStarted,
}: {
  app: Application;
  afterUninstall?: boolean;
  onClose: () => void;
  onDeleted: () => void;
  onUninstallStarted?: (runId: string) => void;
}) {
  const [deployed, setDeployed] = useState<Deployed | null>(
    afterUninstall ? "keep" : null,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const managed = useManagedStateComponents(app.id, !afterUninstall);

  async function runDelete() {
    setBusy(true);
    setError(null);
    const { error } = await api.DELETE("/api/applications/{id}", {
      params: { path: { id: app.id } },
    });
    if (error) {
      setError(error.message ?? "Could not delete this application");
      setBusy(false);
      return;
    }
    onDeleted();
  }

  async function startUninstall() {
    setBusy(true);
    setError(null);
    const { data, error, response } = await api.POST(
      "/api/applications/{id}/runs",
      { params: { path: { id: app.id } }, body: { action: "uninstall" } },
    );
    if (error || !data) {
      setError(runStartError(error, response?.status));
      setBusy(false);
      return;
    }
    onUninstallStarted?.(data.id);
  }

  const uninstalling = deployed === "uninstall";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-application-title"
        className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2
            id="delete-application-title"
            className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight"
          >
            <AlertTriangle className="h-5 w-5 text-red-400" />
            Delete {app.name}
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="text-neutral-500 hover:text-neutral-300 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <p className="text-sm text-neutral-300">
            This permanently removes the application — along with its deploy
            workflow, variables, and run history — from Spacefleet. This cannot
            be undone.
          </p>
          {afterUninstall ? (
            <p className="text-sm text-neutral-400">
              Its uninstall finished, so nothing it deployed is left running.
            </p>
          ) : (
            <fieldset className="space-y-3">
              <legend className="text-sm font-medium text-neutral-100">
                What about everything it deployed?
              </legend>
              <Choice
                checked={deployed === "uninstall"}
                onChange={() => setDeployed("uninstall")}
                disabled={busy}
                title="Uninstall it first"
              >
                Starts an uninstall run that removes every component&apos;s
                release and destroys the infrastructure its OpenTofu components
                manage. Approval gates still apply. You&apos;ll be asked to
                delete the application once the uninstall succeeds.
              </Choice>
              <Choice
                checked={deployed === "keep"}
                onChange={() => setDeployed("keep")}
                disabled={busy}
                title="Leave it running"
              >
                Delete the application now. Everything it deployed stays as it
                is, and Spacefleet stops managing it.
              </Choice>
            </fieldset>
          )}
          {deployed === "keep" && !afterUninstall && managed.length > 0 && (
            <div className="border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
              The OpenTofu state Spacefleet keeps for{" "}
              {managed.map((c, i) => (
                <span key={c.id}>
                  {i > 0 && ", "}
                  <Link
                    to={`/applications/${app.id}/workflow/nodes/${c.id}`}
                    className="font-mono underline-offset-2 hover:underline"
                  >
                    {c.name}
                  </Link>
                </span>
              ))}{" "}
              is deleted with the application, so their resources would keep
              running with nothing to manage them. To manage them elsewhere,
              download each one&apos;s state from its page first.
            </div>
          )}
          {error && <p className="text-sm text-red-400">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-800 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="text-sm text-neutral-400 hover:text-neutral-100 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void (uninstalling ? startUninstall() : runDelete())}
            disabled={busy || deployed === null}
            className="inline-flex items-center gap-1.5 bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
          >
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {uninstalling
              ? busy
                ? "Starting…"
                : "Start uninstall"
              : busy
                ? "Deleting…"
                : "Delete"}
          </button>
        </div>
      </div>
    </div>
  );
}

// useManagedStateComponents lists the application's OpenTofu components on
// managed state — whose state goes with the application — for the "Leave it
// running" warning. Empty until loaded, or when not wanted.
function useManagedStateComponents(
  appId: string,
  enabled: boolean,
): { id: string; name: string }[] {
  const [managed, setManaged] = useState<{ id: string; name: string }[]>([]);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}/workflow", {
        params: { path: { id: appId } },
      });
      // Best effort: without the workflow there's just no warning.
      if (cancelled || !Array.isArray(data?.stages)) return;
      setManaged(
        data.stages
          .flatMap((st) => st.components)
          .filter((c) => c.type === "terraform" && c.config.backend === "spacefleet")
          .map((c) => ({ id: c.id, name: c.name })),
      );
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, enabled]);
  return managed;
}

// Choice is one radio answer of a delete dialog's "what about what it
// deployed?" question: a title with its consequences beneath.
export function Choice({
  checked,
  onChange,
  disabled,
  title,
  children,
}: {
  checked: boolean;
  onChange: () => void;
  disabled: boolean;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex items-start gap-2 text-sm text-neutral-300">
      <input
        type="radio"
        name="deployed"
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        className="mt-0.5 h-3.5 w-3.5 accent-white"
      />
      <span>
        <span className="font-medium text-neutral-100">{title}</span>
        <span className="block text-xs text-neutral-400">{children}</span>
      </span>
    </label>
  );
}
