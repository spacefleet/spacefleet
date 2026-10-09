import { useCallback, useEffect, useState } from "react";
import {
  AlertTriangle,
  ArrowUpCircle,
  Info,
  Loader2,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ClusterCapabilities } from "./ClusterCapabilities";
import { useObjectStream } from "../lib/useObjectStream";

type TektonStatus = components["schemas"]["TektonStatus"];
type TektonPluginCache = components["schemas"]["TektonPluginCache"];
type InstallStatus = TektonStatus["status"];

// Statuses where an install/uninstall job is in flight — the install stream is
// open and the UI shows live progress.
const IN_FLIGHT: InstallStatus[] = ["installing", "upgrading", "uninstalling"];

interface Props {
  clusterId: string;
  canEdit: boolean;
  // When false, omit the embedded ClusterCapabilities "Readiness" report. The
  // cluster detail page already shows the full capability report as its own
  // section, so the panel would otherwise render it twice.
  showCapabilities?: boolean;
}

// TektonPanel manages one cluster's job-running setup. It separates two concerns
// that used to be tangled together:
//   1. the primary runner control — an On/Off "Use this cluster as a runner"
//      state with a button that installs Tekton on demand and designates the
//      cluster as a runner (or clears the designation), always behind a confirm
//      dialog because installing touches the whole cluster,
//   2. an Engine block: the Tekton install itself (version, provenance, and the
//      managed-only upgrade/remove lifecycle), kept visually distinct from the
//      runner control so it's clear it's about the install, not the
//      designation.
// A transient status line above them carries live progress while an install is
// in flight (or the error on failure). Install/upgrade/uninstall run as
// background jobs followed live over an SSE stream (no polling).
export function TektonPanel({
  clusterId,
  canEdit,
  showCapabilities = true,
}: Props) {
  const [status, setStatus] = useState<TektonStatus | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  // Action errors (enable/disable/upgrade/uninstall) are kept separate from the
  // load error: a failed action shows inline and is retryable, whereas a load
  // error blanks the panel (there's nothing to show). Cleared at the start of
  // each action and on a successful load.
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  // The status the runner confirm dialog was opened against (null when
  // closed). Snapshotted so the dialog's wording and action don't flip when
  // the confirmed change lands while it is still open.
  const [runnerConfirm, setRunnerConfirm] = useState<TektonStatus | null>(null);

  const load = useCallback(async () => {
    setLoadError(null);
    const { data, error } = await api.GET("/api/clusters/{id}/tekton", {
      params: { path: { id: clusterId } },
    });
    if (error) {
      setLoadError(error.message ?? "Could not load Tekton status");
      return;
    }
    if (data) {
      setStatus(data);
      setActionError(null);
    }
  }, [clusterId]);

  useEffect(() => {
    void load();
  }, [load]);

  const inFlight = status ? IN_FLIGHT.includes(status.status) : false;

  // Follow install progress live while a job is in flight. The stream carries
  // the install lifecycle (status/message/version) from the row; presence
  // (present/controller_ready/detected_version) comes from the last full load,
  // so merge only the lifecycle fields and re-load when the install settles.
  const { value: streamed } = useObjectStream<TektonStatus>(
    `/api/clusters/${clusterId}/tekton/stream`,
    inFlight,
  );
  useEffect(() => {
    if (!streamed) return;
    setStatus((prev) =>
      prev
        ? {
            ...prev,
            status: streamed.status,
            status_message: streamed.status_message,
            installed_version: streamed.installed_version,
            job_id: streamed.job_id,
            enabled: streamed.enabled,
          }
        : streamed,
    );
    if (!IN_FLIGHT.includes(streamed.status)) void load();
  }, [streamed, load]);

  // settle folds an enable/disable/upgrade/uninstall response into state. Those
  // responses carry the stored row only (presence is nil), so when the result
  // isn't an in-flight job — e.g. flipping the flag on an already-installed
  // cluster — we re-load to refresh live presence (an in-flight job instead
  // refreshes via the stream). Without this the Engine block and status line go
  // briefly stale right after a toggle.
  const settle = useCallback(
    (data: TektonStatus) => {
      setStatus(data);
      if (!IN_FLIGHT.includes(data.status)) void load();
    },
    [load],
  );

  // onEnable/onDisable run from the runner confirm dialog, which shows a
  // failure itself (so the operator can retry or back out): they return the
  // error message, or null once the change is applied.
  async function onEnable(): Promise<string | null> {
    setActionError(null);
    const { data, error } = await api.POST("/api/clusters/{id}/tekton/enable", {
      params: { path: { id: clusterId } },
    });
    if (error)
      return error.message ?? "Could not set this cluster up as a runner";
    if (data) settle(data);
    return null;
  }

  async function onDisable(): Promise<string | null> {
    setActionError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/tekton/disable",
      { params: { path: { id: clusterId } } },
    );
    if (error)
      return error.message ?? "Could not stop using this cluster as a runner";
    if (data) settle(data);
    return null;
  }

  async function onUpgrade() {
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/tekton/upgrade",
      { params: { path: { id: clusterId } } },
    );
    setBusy(false);
    if (!error && data) settle(data);
    else if (error) setActionError(error.message ?? "Could not upgrade Tekton");
  }

  async function onUninstall() {
    setConfirmingDelete(false);
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/tekton/uninstall",
      { params: { path: { id: clusterId } } },
    );
    setBusy(false);
    if (!error && data) settle(data);
    else if (error) setActionError(error.message ?? "Could not remove Tekton");
  }

  if (loadError) {
    return <p className="p-4 text-sm text-red-400">{loadError}</p>;
  }
  if (!status) {
    return <p className="p-4 text-sm text-neutral-400">Loading…</p>;
  }

  // Update/delete act only on a Spacefleet-managed install. The server compares
  // the live install against what this Spacefleet would apply — the pinned
  // Tekton version plus the install manifest's revision — so update_available
  // also covers a changed footprint at the same version (and installs that
  // predate revision stamping). A version difference reads as an upgrade; a
  // same-version manifest change reads as a sync.
  const updateAvailable = status.update_available;
  const versionChange =
    !!status.detected_version &&
    status.detected_version !== status.pinned_version;
  const canRemove = status.managed && status.present;
  const unmanaged = status.present && !status.managed;

  return (
    <>
      <div className="divide-y divide-neutral-800">
        {/* The two sections below stand on their own for steady states; the
          status line is only worth its space while a job is in flight (live
          progress) or after a failure (the error). */}
        {(inFlight || status.status === "failed") && (
          <StatusLine status={status} />
        )}

        {/* Action errors render inline and leave the controls in place so the
          operator can read the message and retry — distinct from a load error,
          which blanks the panel. */}
        {actionError && (
          <p className="p-4 text-sm text-red-400">{actionError}</p>
        )}

        {/* Primary control: turns this cluster into a runner. Turning it on
          installs Tekton if needed; off leaves the install in place. Either
          way the button only opens a confirm dialog that spells out the
          change — installing is a cluster-wide modification that shouldn't
          happen on a stray click. */}
        <div className="p-4">
          <RunnerControl
            enabled={status.enabled}
            present={status.present}
            canEdit={canEdit}
            disabled={busy || inFlight}
            onClick={() => setRunnerConfirm(status)}
          />
          <p className="mt-2 text-xs text-neutral-400">
            {status.enabled
              ? status.present
                ? "Applications can choose this cluster as their runner. Stopping leaves Tekton installed."
                : "Tekton will be installed on this cluster so it can act as a runner."
              : status.present
                ? "Tekton is already installed. Use this cluster as a runner to let applications run their workflow jobs on it."
                : "Setting this cluster up as a runner installs Tekton so applications can run their workflow jobs on it."}
          </p>
        </div>

        {/* Engine block: the Tekton install itself — distinct from the runner
          control so it's clear this is about the engine (version, provenance,
          lifecycle), not the designation. Hidden entirely until Tekton exists. */}
        {status.present && (
          <div className="p-4">
            <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
              Engine
            </h3>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-neutral-100">
                Tekton{" "}
                {status.detected_version ?? status.installed_version ?? ""}
              </span>
              <ProvenanceBadge managed={status.managed} />
              {updateAvailable && (
                <span className="inline-flex items-center gap-1 border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-xs font-medium text-amber-300">
                  <ArrowUpCircle className="h-3.5 w-3.5" />
                  Update available
                </span>
              )}
            </div>
            <p className="mt-1 text-xs text-neutral-400">
              {status.controller_ready
                ? "Controller ready"
                : "Controller not ready"}
              {/* The pinned version is the upgrade target Spacefleet manages — only
                meaningful for a Spacefleet-managed install, not an existing one. */}
              {!unmanaged && (
                <>
                  {" · "}Spacefleet installs {status.pinned_version}
                </>
              )}
            </p>
            <p className="mt-1 text-xs text-neutral-500">
              {unmanaged
                ? "Installed outside Spacefleet."
                : "Installed by Spacefleet."}
            </p>
            {updateAvailable && (
              <p className="mt-1 text-xs text-amber-300">
                {versionChange
                  ? `A newer Tekton (${status.pinned_version}) is available.`
                  : "This install differs from what this version of Spacefleet sets up — sync it to bring it up to date."}
              </p>
            )}

            {canEdit && (updateAvailable || canRemove) && (
              <div className="mt-3 flex flex-wrap items-center gap-2">
                {updateAvailable && (
                  <button
                    type="button"
                    onClick={() => void onUpgrade()}
                    disabled={busy || inFlight}
                    title={
                      versionChange
                        ? `Upgrade to ${status.pinned_version}`
                        : "Re-apply the managed install so it matches what Spacefleet expects"
                    }
                    className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
                  >
                    <ArrowUpCircle className="h-3.5 w-3.5" />
                    {versionChange
                      ? `Upgrade to ${status.pinned_version}`
                      : "Sync install"}
                  </button>
                )}
                {canRemove &&
                  (confirmingDelete ? (
                    <>
                      <button
                        type="button"
                        onClick={() => void onUninstall()}
                        disabled={busy || inFlight}
                        className="inline-flex items-center gap-1.5 bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Confirm remove
                      </button>
                      <button
                        type="button"
                        onClick={() => setConfirmingDelete(false)}
                        disabled={busy}
                        className="inline-flex items-center px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
                      >
                        Cancel
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      onClick={() => setConfirmingDelete(true)}
                      disabled={busy || inFlight}
                      title="Remove Tekton from this cluster"
                      className="inline-flex items-center gap-1.5 border border-red-500/40 px-3 py-1.5 text-sm text-red-300 hover:bg-red-500/10 disabled:opacity-50"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                      Remove Tekton
                    </button>
                  ))}
              </div>
            )}
          </div>
        )}

        {/* Provider plugin cache: a per-cluster volume OpenTofu steps share so
          providers download once. Only meaningful once Tekton is present (the
          claim lives in the jobs namespace the install creates). */}
        {status.present && (
          <PluginCacheSection
            clusterId={clusterId}
            cache={status.plugin_cache ?? null}
            canEdit={canEdit}
            onSaved={settle}
          />
        )}

        {showCapabilities && (
          <div className="p-4">
            <h3 className="pb-1 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
              Readiness
            </h3>
            <ClusterCapabilities clusterId={clusterId} bordered={false} />
          </div>
        )}
      </div>
      {runnerConfirm && (
        <RunnerDialog
          status={runnerConfirm}
          onConfirm={runnerConfirm.enabled ? onDisable : onEnable}
          onClose={() => setRunnerConfirm(null)}
        />
      )}
    </>
  );
}

// StatusLine surfaces transient state the two sections below can't show on their
// own: live progress while an install/upgrade/uninstall job is in flight, and
// the error after a failure. Steady states (running / off / not set up) are
// already clear from the runner and Engine sections, so the panel doesn't render
// this line for them.
function StatusLine({ status }: { status: TektonStatus }) {
  const inFlight = IN_FLIGHT.includes(status.status);
  let Icon = AlertTriangle;
  let tone = "text-red-300";
  let headline = "Setup failed";

  if (inFlight) {
    Icon = Loader2;
    tone = "text-blue-300";
    headline =
      status.status === "installing"
        ? "Setting up runner…"
        : status.status === "upgrading"
          ? "Upgrading Tekton…"
          : "Removing Tekton…";
  }

  return (
    <div className="p-4">
      <div className={`flex items-center gap-2 ${tone}`}>
        <Icon
          className={`h-5 w-5 shrink-0 ${inFlight ? "animate-spin" : ""}`}
        />
        <span className="text-sm font-medium">{headline}</span>
      </div>
      {status.status_message && (
        <p className="mt-1 pl-7 text-xs text-neutral-400">
          {status.status_message}
        </p>
      )}
    </div>
  );
}

// RunnerControl is the primary control: the cluster's On/Off runner state
// and, for editors, the button that opens the confirm dialog to change it.
// Viewers see only the state. rounded-full is the one curve the brand allows,
// used for the status dot.
function RunnerControl({
  enabled,
  present,
  canEdit,
  disabled,
  onClick,
}: {
  enabled: boolean;
  present: boolean;
  canEdit: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      <div className="flex items-center gap-3">
        <span className="text-sm font-medium text-neutral-100">
          Use this cluster as a runner
        </span>
        <span
          className={`inline-flex items-center gap-1.5 text-xs font-medium ${
            enabled ? "text-green-300" : "text-neutral-400"
          }`}
        >
          <span
            className={`h-2 w-2 rounded-full ${
              enabled ? "bg-green-600" : "bg-neutral-600"
            }`}
          />
          {enabled ? "On" : "Off"}
        </span>
      </div>
      {canEdit && (
        <button
          type="button"
          onClick={onClick}
          disabled={disabled}
          className={
            enabled
              ? "border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
              : "bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
          }
        >
          {enabled
            ? "Stop using as runner"
            : present
              ? "Use as runner"
              : "Set up as runner"}
        </button>
      )}
    </div>
  );
}

// RunnerDialog confirms a change to the runner designation and explains what
// it will do. The heavy case is setting up a runner where Tekton isn't
// installed yet: Spacefleet installs Tekton, which adds cluster-scoped
// resources (CRDs, admission webhooks, RBAC) and new namespaces — so the
// dialog lists that footprint before anything is applied. Turning on over an
// existing install, or turning off, only flips the designation; the dialog
// says so. A failed request keeps the dialog open with the error.
function RunnerDialog({
  status,
  onConfirm,
  onClose,
}: {
  status: TektonStatus;
  onConfirm: () => Promise<string | null>;
  onClose: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const turningOn = !status.enabled;
  const installs = turningOn && !status.present;
  const title = !turningOn
    ? "Stop using this cluster as a runner"
    : installs
      ? "Install Tekton and set up this runner?"
      : "Use this cluster as a runner";
  const confirmLabel = !turningOn
    ? "Stop using as runner"
    : installs
      ? "Install Tekton"
      : "Use as runner";

  // Close on Escape, but not mid-request — the outcome would be lost.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !submitting) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, submitting]);

  async function confirm() {
    setSubmitting(true);
    setError(null);
    const err = await onConfirm();
    if (err) {
      setError(err);
      setSubmitting(false);
      return;
    }
    onClose();
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="runner-dialog-title"
        className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2
            id="runner-dialog-title"
            className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight"
          >
            {installs && <AlertTriangle className="h-5 w-5 text-amber-400" />}
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="text-neutral-500 hover:text-neutral-300 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-3 px-5 py-4 text-sm text-neutral-300">
          {installs ? (
            <>
              <p>
                Spacefleet will install Tekton Pipelines {status.pinned_version}{" "}
                on this cluster so it can act as a runner for applications&apos;
                workflow jobs. This is a cluster-wide change that adds:
              </p>
              <ul className="list-disc space-y-1 pl-5">
                <li>
                  Tekton&apos;s controllers and webhook, in the{" "}
                  <code className="font-mono text-xs">tekton-pipelines</code>{" "}
                  and{" "}
                  <code className="font-mono text-xs">
                    tekton-pipelines-resolvers
                  </code>{" "}
                  namespaces
                </li>
                <li>
                  Cluster-scoped custom resource definitions, admission
                  webhooks, and RBAC roles
                </li>
                <li>
                  A <code className="font-mono text-xs">spacefleet-jobs</code>{" "}
                  namespace where workflow jobs run
                </li>
              </ul>
              <p className="text-neutral-400">
                The install runs in the background and its progress shows here.
                You can remove Tekton later from the Engine section.
              </p>
            </>
          ) : turningOn ? (
            <>
              <p>
                Tekton{" "}
                {status.detected_version ?? status.installed_version ?? ""} is
                already installed on this cluster, so nothing new is installed.
              </p>
              <p>
                Applications will be able to choose this cluster as their
                runner, and their workflow jobs will run here.
              </p>
            </>
          ) : (
            <>
              <p>
                Applications will no longer be able to choose this cluster as
                their runner. Applications already using it are not changed.
              </p>
              <p className="text-neutral-400">
                Tekton stays installed.
                {status.managed &&
                  status.present &&
                  " To remove it as well, use Remove Tekton in the Engine section."}
              </p>
            </>
          )}
          {error && <p className="text-red-400">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-800 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="text-sm text-neutral-400 hover:text-neutral-100 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void confirm()}
            disabled={submitting}
            className="inline-flex items-center gap-1.5 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
          >
            {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

// ProvenanceBadge says who owns the running Tekton: a Spacefleet-managed install
// (eligible for in-app upgrade/delete) or one that pre-existed on the cluster
// (Spacefleet never touches it). Shown only when Tekton is actually present.
function ProvenanceBadge({ managed }: { managed: boolean }) {
  return (
    <span className="inline-flex items-center gap-1 border border-neutral-700 px-2 py-0.5 text-xs font-medium text-neutral-300">
      {managed ? (
        <>
          <ShieldCheck className="h-3.5 w-3.5" />
          Managed by Spacefleet
        </>
      ) : (
        <>
          <Info className="h-3.5 w-3.5" />
          Existing install
        </>
      )}
    </span>
  );
}

// PluginCacheSection configures the OpenTofu provider plugin cache on the
// runner: shows the current claim (size and class) with a remove action, or a
// small form to create one. Saving calls the API, which creates or deletes the
// claim on the cluster right away and returns the refreshed status.
function PluginCacheSection({
  clusterId,
  cache,
  canEdit,
  onSaved,
}: {
  clusterId: string;
  cache: TektonPluginCache | null;
  canEdit: boolean;
  onSaved: (status: TektonStatus) => void;
}) {
  const [size, setSize] = useState("20Gi");
  const [storageClass, setStorageClass] = useState("");
  const [newSize, setNewSize] = useState(cache?.size ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(next: { size: string; storage_class?: string }) {
    setBusy(true);
    setError(null);
    const { data, error } = await api.PUT(
      "/api/clusters/{id}/tekton/plugin-cache",
      { params: { path: { id: clusterId } }, body: next },
    );
    setBusy(false);
    if (error || !data) {
      setError(error?.message ?? "Could not update the plugin cache");
      return;
    }
    onSaved(data);
  }

  return (
    <div className="p-4">
      <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
        Provider plugin cache
      </h3>
      <p className="mt-1 text-xs text-neutral-400">
        A shared volume OpenTofu steps on this cluster use as their provider
        plugin cache, so a provider is downloaded once per cluster instead of
        once per run.
      </p>
      {cache ? (
        <div className="mt-2 flex flex-wrap items-center gap-3 text-sm">
          <span className="text-neutral-100">
            <span className="font-medium">{cache.size}</span>
            {cache.storage_class ? (
              <span className="text-neutral-400"> · {cache.storage_class}</span>
            ) : (
              <span className="text-neutral-400"> · default storage class</span>
            )}
          </span>
          {canEdit && (
            <>
              <form
                className="flex items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void save({
                    size: newSize.trim(),
                    storage_class: cache.storage_class || undefined,
                  });
                }}
              >
                <input
                  type="text"
                  aria-label="New plugin cache size"
                  value={newSize}
                  onChange={(e) => setNewSize(e.target.value)}
                  className="w-24 border border-neutral-700 px-2 py-1.5 font-mono text-sm text-neutral-100"
                />
                <button
                  type="submit"
                  disabled={busy || newSize.trim() === "" || newSize.trim() === cache.size}
                  className="border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
                >
                  Resize
                </button>
              </form>
              <button
                type="button"
                onClick={() => void save({ size: "" })}
                disabled={busy}
                className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Remove cache
              </button>
              <p className="basis-full text-xs text-neutral-400">
                The cache can grow in place when its storage class allows volume
                expansion. To shrink it or change the class, remove it and set
                it up again.
              </p>
            </>
          )}
        </div>
      ) : canEdit ? (
        <form
          className="mt-2 flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void save({
              size: size.trim(),
              storage_class: storageClass.trim() || undefined,
            });
          }}
        >
          <label className="flex flex-col gap-1 text-xs text-neutral-300">
            Size
            <input
              type="text"
              aria-label="Plugin cache size"
              value={size}
              onChange={(e) => setSize(e.target.value)}
              className="w-28 border border-neutral-700 px-2 py-1.5 font-mono text-sm text-neutral-100"
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-neutral-300">
            Storage class
            <input
              type="text"
              aria-label="Plugin cache storage class"
              value={storageClass}
              placeholder="(cluster default)"
              onChange={(e) => setStorageClass(e.target.value)}
              className="w-48 border border-neutral-700 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans"
            />
          </label>
          <button
            type="submit"
            disabled={busy || size.trim() === ""}
            className="bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
          >
            {busy ? "Creating…" : "Create cache"}
          </button>
          <p className="basis-full text-xs text-neutral-400">
            The storage class must support ReadWriteMany so steps on every node
            can share the volume.
          </p>
        </form>
      ) : (
        <p className="mt-2 text-sm text-neutral-400">No cache configured.</p>
      )}
      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
    </div>
  );
}
