import { useState, type ReactNode } from "react";
import { AlertTriangle, Download, Loader2, X } from "lucide-react";
import { api } from "../../api/client";
import { Choice } from "../DeleteApplicationDialog";
import { downloadComponentState } from "../../lib/stateDownload";
import { helmReleaseName, stateLocation } from "./componentDetails";
import type { EditableComponent } from "./ComponentFields";
import { runStartError } from "./runAction";

// What happens to what the component deployed when it is deleted.
type Deployed = "uninstall" | "keep";

// DeleteComponentDialog removes a component from its workflow — and, like
// deleting an application, first asks what happens to what it deployed:
//
//   - "uninstall": destroy (OpenTofu) or uninstall (Helm, Manifest) just this
//     component first. That is a run of its own which always waits for
//     approval, so the dialog only starts it and hands the run id back; the
//     run view offers the delete once it succeeds.
//   - "keep": delete now, leaving what it deployed running — with a warning
//     specific to what is left behind. For OpenTofu on managed state that
//     includes the state itself, which is deleted with the component (the
//     save is sent with allowStateDeletion), so the dialog offers it as a
//     download first.
//
// Neither is preselected. With afterUninstall set (opened once that run
// succeeded) the question is skipped: nothing is left to uninstall.
export function DeleteComponentDialog({
  appId,
  appName,
  component,
  clusterName,
  afterUninstall = false,
  onClose,
  onDelete,
  onDeleted,
  onUninstallStarted,
}: {
  appId: string;
  appName?: string;
  component: EditableComponent;
  clusterName: (id: string) => string | undefined;
  afterUninstall?: boolean;
  onClose: () => void;
  // Deletes the component (saving the workflow); resolves to the server's
  // message on failure, or null.
  onDelete: (opts: { allowStateDeletion: boolean }) => Promise<string | null>;
  onDeleted: () => void;
  onUninstallStarted: (runId: string) => void;
}) {
  const [deployed, setDeployed] = useState<Deployed | null>(
    afterUninstall ? "keep" : null,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isTofu = component.type === "terraform";
  const managedState = isTofu && component.config.backend === "spacefleet";
  const verb = isTofu ? "destroy" : "uninstall";

  async function runDelete() {
    setBusy(true);
    setError(null);
    const err = await onDelete({
      allowStateDeletion: managedState && !afterUninstall,
    });
    if (err) {
      setError(err);
      setBusy(false);
      return;
    }
    onDeleted();
  }

  async function startUninstall() {
    setBusy(true);
    setError(null);
    const { data, error, response } = await api.POST(
      "/api/applications/{id}/components/{componentId}/runs",
      {
        params: { path: { id: appId, componentId: component.id } },
        body: { action: "uninstall" },
      },
    );
    if (error || !data) {
      setError(runStartError(error, response?.status));
      setBusy(false);
      return;
    }
    onUninstallStarted(data.id);
  }

  const uninstalling = deployed === "uninstall";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-component-title"
        className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2
            id="delete-component-title"
            className="inline-flex min-w-0 items-center gap-2 text-lg font-semibold tracking-tight"
          >
            <AlertTriangle className="h-5 w-5 shrink-0 text-red-400" />
            <span className="truncate">Delete {component.name}</span>
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
            This removes the component from the workflow, along with its
            variables. Its run history stays with the application.
          </p>
          {afterUninstall ? (
            <p className="text-sm text-neutral-400">
              Its {verb} finished, so nothing it deployed is left running.
            </p>
          ) : (
            <fieldset className="space-y-3">
              <legend className="text-sm font-medium text-neutral-100">
                What about what it deployed?
              </legend>
              <Choice
                checked={deployed === "uninstall"}
                onChange={() => setDeployed("uninstall")}
                disabled={busy}
                title={isTofu ? "Destroy it first" : "Uninstall it first"}
              >
                {uninstallSummary(component, appName, clusterName)} It always
                waits for approval, and you&apos;ll be asked to delete the
                component once it succeeds.
              </Choice>
              <Choice
                checked={deployed === "keep"}
                onChange={() => setDeployed("keep")}
                disabled={busy}
                title="Leave it running"
              >
                Delete the component now. What it deployed stays as it is, and
                Spacefleet stops managing it.
              </Choice>
            </fieldset>
          )}
          {deployed === "keep" && !afterUninstall && (
            <LeftBehind
              appId={appId}
              appName={appName}
              component={component}
              clusterName={clusterName}
            />
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
                : `Start ${verb} for approval`
              : busy
                ? "Deleting…"
                : "Delete component"}
          </button>
        </div>
      </div>
    </div>
  );
}

// uninstallSummary says what the destroy-or-uninstall run removes.
function uninstallSummary(
  c: EditableComponent,
  appName: string | undefined,
  clusterName: (id: string) => string | undefined,
): string {
  const cluster = clusterLabel(c, clusterName);
  switch (c.type) {
    case "terraform":
      return "Starts a run that destroys every resource this component manages, and only those.";
    case "helm":
      return `Starts a run that uninstalls release ${helmReleaseName(c, appName)} from ${c.target_namespace || "its namespace"} on ${cluster}.`;
    case "manifest":
      return `Starts a run that deletes what was applied from ${c.config.path || "its path"} on ${cluster}.`;
  }
}

function clusterLabel(
  c: EditableComponent,
  clusterName: (id: string) => string | undefined,
): string {
  const id = c.target_cluster_id;
  return id ? (clusterName(id) ?? id) : "its cluster";
}

// LeftBehind is the "Leave it running" warning: exactly what keeps running,
// and — for OpenTofu — what becomes of its state.
function LeftBehind({
  appId,
  appName,
  component: c,
  clusterName,
}: {
  appId: string;
  appName?: string;
  component: EditableComponent;
  clusterName: (id: string) => string | undefined;
}) {
  const cluster = clusterLabel(c, clusterName);
  let body: ReactNode;
  switch (c.type) {
    case "terraform": {
      const backend = c.config.backend || "s3";
      if (backend === "spacefleet") {
        body = (
          <>
            <p>
              Its resources keep running, and its <strong>state is
              deleted</strong> with it: Spacefleet can&apos;t manage them
              again, and OpenTofu would need them imported to track them.
            </p>
            <StateDownload appId={appId} componentId={c.id} />
          </>
        );
      } else {
        const where = stateLocation(backend, c.config.backend_config);
        body = (
          <p>
            Its resources keep running. Its state stays in your bucket
            {where && (
              <>
                {" "}
                (<span className="font-mono">{where}</span>)
              </>
            )}{" "}
            — a component pointed at it picks them back up.
          </p>
        );
      }
      break;
    }
    case "helm":
      body = (
        <p>
          Release{" "}
          <span className="font-mono">{helmReleaseName(c, appName)}</span> in{" "}
          <span className="font-mono">{c.target_namespace}</span> on {cluster}{" "}
          keeps running.
        </p>
      );
      break;
    case "manifest":
      body = (
        <p>
          What was applied from{" "}
          <span className="font-mono">{c.config.path || "its path"}</span>{" "}
          keeps running on {cluster}.
        </p>
      );
      break;
  }
  return (
    <div className="space-y-2 border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
      {body}
    </div>
  );
}

// StateDownload offers the managed state as a file before it is deleted.
function StateDownload({
  appId,
  componentId,
}: {
  appId: string;
  componentId: string;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <p>
      <button
        type="button"
        onClick={() => {
          setBusy(true);
          setError(null);
          void downloadComponentState(appId, componentId).then((err) => {
            setBusy(false);
            setError(err);
          });
        }}
        disabled={busy}
        className="inline-flex items-center gap-1.5 underline-offset-2 hover:underline disabled:opacity-50"
      >
        <Download className="h-3.5 w-3.5" />
        {busy ? "Downloading…" : "Download the state first"}
      </button>{" "}
      <span className="text-xs text-amber-300">
        to keep managing them elsewhere.
      </span>
      {error && <span className="block text-xs text-red-300">{error}</span>}
    </p>
  );
}
