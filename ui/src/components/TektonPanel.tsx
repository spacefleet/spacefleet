import { useCallback, useEffect, useState } from "react";
import {
  AlertTriangle,
  ArrowUpCircle,
  Info,
  Loader2,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ClusterCapabilities } from "./ClusterCapabilities";
import { useObjectStream } from "../lib/useObjectStream";

type TektonStatus = components["schemas"]["TektonStatus"];
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
//   1. one primary On/Off switch — "Run jobs on this cluster" — which installs
//      Tekton on demand and designates the cluster as a job runner,
//   2. an Engine block: the Tekton install itself (version, provenance, and the
//      managed-only upgrade/remove lifecycle), kept visually distinct from the
//      switch so it's clear it's about the install, not the designation.
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

  async function onEnable() {
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/tekton/enable",
      { params: { path: { id: clusterId } } },
    );
    setBusy(false);
    if (!error && data) settle(data);
    else if (error)
      setActionError(error.message ?? "Could not enable job running");
  }

  async function onDisable() {
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/tekton/disable",
      { params: { path: { id: clusterId } } },
    );
    setBusy(false);
    if (!error && data) settle(data);
    else if (error)
      setActionError(error.message ?? "Could not turn off job running");
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
    return <p className="p-4 text-sm text-red-600">{loadError}</p>;
  }
  if (!status) {
    return <p className="p-4 text-sm text-neutral-500">Loading…</p>;
  }

  const installed = status.status === "installed";
  // Upgrade/delete act only on a Spacefleet-managed install. Upgrade is offered
  // when the running version differs from the version we'd install.
  const upgradeAvailable =
    status.managed &&
    installed &&
    !!status.detected_version &&
    status.detected_version !== status.pinned_version;
  const canRemove = status.managed && status.present;
  const unmanaged = status.present && !status.managed;

  return (
    <div className="divide-y divide-neutral-200">
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
        <p className="p-4 text-sm text-red-600">{actionError}</p>
      )}

      {/* Primary control: the single switch that turns this cluster into a job
          runner. Flipping it on installs Tekton if needed; off leaves the
          install in place (stated in the helper text). */}
      <div className="p-4">
        <RunJobsSwitch
          enabled={status.enabled}
          inFlight={inFlight}
          disabled={!canEdit || busy || inFlight}
          onToggle={() => void (status.enabled ? onDisable() : onEnable())}
        />
        <p className="mt-2 text-xs text-neutral-500">
          {status.enabled
            ? status.present
              ? "This cluster is designated to run jobs. Turning it off leaves Tekton installed."
              : "Tekton will be installed on this cluster so it can run jobs."
            : status.present
              ? "Turn on to let this cluster run jobs. Tekton is already installed."
              : "Turn on to install Tekton and let this cluster run jobs."}
        </p>
      </div>

      {/* Engine block: the Tekton install itself — distinct from the switch so
          it's clear this is about the engine (version, provenance, lifecycle),
          not the run-jobs designation. Hidden entirely until Tekton exists. */}
      {status.present && (
        <div className="p-4">
          <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            Engine
          </h3>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-neutral-900">
              Tekton {status.detected_version ?? status.installed_version ?? ""}
            </span>
            <ProvenanceBadge managed={status.managed} />
          </div>
          <p className="mt-1 text-xs text-neutral-500">
            {status.controller_ready
              ? "Controller ready"
              : "Controller not ready"}
            {/* The pinned version is the upgrade target Spacefleet manages — only
                meaningful for a Spacefleet-managed install, not an existing one. */}
            {!unmanaged && <>{" · "}Spacefleet installs {status.pinned_version}</>}
          </p>
          <p className="mt-1 text-xs text-neutral-400">
            {unmanaged ? "Installed outside Spacefleet." : "Installed by Spacefleet."}
          </p>

          {canEdit && (upgradeAvailable || canRemove) && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              {upgradeAvailable && (
                <button
                  type="button"
                  onClick={() => void onUpgrade()}
                  disabled={busy || inFlight}
                  title={`Upgrade to ${status.pinned_version}`}
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  <ArrowUpCircle className="h-3.5 w-3.5" />
                  Upgrade to {status.pinned_version}
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
                      className="inline-flex items-center px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
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
                    className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                    Remove Tekton
                  </button>
                ))}
            </div>
          )}
        </div>
      )}

      {showCapabilities && (
        <div className="p-4">
          <h3 className="pb-1 text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            Readiness
          </h3>
          <ClusterCapabilities clusterId={clusterId} bordered={false} />
        </div>
      )}
    </div>
  );
}

// StatusLine surfaces transient state the two sections below can't show on their
// own: live progress while an install/upgrade/uninstall job is in flight, and
// the error after a failure. Steady states (running / off / not set up) are
// already clear from the switch and Engine sections, so the panel doesn't render
// this line for them.
function StatusLine({ status }: { status: TektonStatus }) {
  const inFlight = IN_FLIGHT.includes(status.status);
  let Icon = AlertTriangle;
  let tone = "text-red-700";
  let headline = "Setup failed";

  if (inFlight) {
    Icon = Loader2;
    tone = "text-blue-700";
    headline =
      status.status === "installing"
        ? "Setting up job running…"
        : status.status === "upgrading"
          ? "Upgrading Tekton…"
          : "Removing Tekton…";
  }

  return (
    <div className="p-4">
      <div className={`flex items-center gap-2 ${tone}`}>
        <Icon className={`h-5 w-5 shrink-0 ${inFlight ? "animate-spin" : ""}`} />
        <span className="text-sm font-medium">{headline}</span>
      </div>
      {status.status_message && (
        <p className="mt-1 pl-7 text-xs text-neutral-500">
          {status.status_message}
        </p>
      )}
    </div>
  );
}

// RunJobsSwitch is the primary control: a labeled on/off switch. rounded-full is
// the one curve the brand allows, so the toggle reads as a toggle.
function RunJobsSwitch({
  enabled,
  inFlight,
  disabled,
  onToggle,
}: {
  enabled: boolean;
  inFlight: boolean;
  disabled: boolean;
  onToggle: () => void;
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <span className="text-sm font-medium text-neutral-900">
        Run jobs on this cluster
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={enabled}
        aria-label="Run jobs on this cluster"
        onClick={onToggle}
        disabled={disabled}
        className={`relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
          enabled ? "bg-green-600" : "bg-neutral-300"
        }`}
      >
        <span
          className={`inline-flex h-5 w-5 items-center justify-center rounded-full bg-white shadow transition-transform ${
            enabled ? "translate-x-5" : "translate-x-0.5"
          }`}
        >
          {inFlight && (
            <Loader2 className="h-3 w-3 animate-spin text-neutral-500" />
          )}
        </span>
      </button>
    </div>
  );
}

// ProvenanceBadge says who owns the running Tekton: a Spacefleet-managed install
// (eligible for in-app upgrade/delete) or one that pre-existed on the cluster
// (Spacefleet never touches it). Shown only when Tekton is actually present.
function ProvenanceBadge({ managed }: { managed: boolean }) {
  return (
    <span className="inline-flex items-center gap-1 border border-neutral-300 px-2 py-0.5 text-xs font-medium text-neutral-600">
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
