import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Download } from "lucide-react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { downloadComponentState } from "../../lib/stateDownload";
import { OutputsTable } from "./OutputsTable";
import { ResourcesTable } from "./ResourcesTable";

type ComponentState = components["schemas"]["ComponentState"];
type DriftStatus = components["schemas"]["DriftStatus"];
type ManagedStateVersion = components["schemas"]["ManagedStateVersion"];
type StateLock = components["schemas"]["StateLock"];

// ComponentStatePanel is an OpenTofu component's persistent "what do I own"
// view: the outputs and the managed-resource inventory its last successful
// apply recorded, with a link to the run that recorded them. It reads the
// component-state endpoint; a component that has never applied successfully
// (404) shows a quiet placeholder rather than an error. The guarded state
// operations are a card of their own (ComponentOperations).
export function ComponentStatePanel({
  appId,
  componentId,
  canEdit = false,
  managedBackend = false,
  onReleaseLock,
}: {
  appId: string;
  componentId: string;
  // Editor or above: offers the state download and, on a stuck lock, the
  // release button. Viewers see the recorded state only.
  canEdit?: boolean;
  // The component keeps its state in Spacefleet (the managed backend), so
  // its current state version is shown, and editors can download it. A
  // cloud backend's state is in its own bucket.
  managedBackend?: boolean;
  // Hands a stuck lock's id to the Operations card, which opens force-unlock
  // with it filled in.
  onReleaseLock?: (lockId: string) => void;
}) {
  const [state, setState] = useState<ComponentState | null>(null);
  const [empty, setEmpty] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<"resources" | "outputs">("resources");

  useEffect(() => {
    let cancelled = false;
    setState(null);
    setEmpty(false);
    setError(null);
    void (async () => {
      const { data, error, response } = await api.GET(
        "/api/applications/{id}/components/{componentId}/state",
        { params: { path: { id: appId, componentId } } },
      );
      if (cancelled) return;
      if (error || !data) {
        if (response?.status === 404) setEmpty(true);
        else setError(error?.message ?? "Could not load this component's state");
        return;
      }
      setState(data);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, componentId]);

  const outputEntries = Object.entries(state?.outputs ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );

  return (
    <div className="mt-6 border border-neutral-800 bg-neutral-900 p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
          State
        </h2>
        {state?.run_id && (
          <p className="text-xs text-neutral-400">
            Recorded by{" "}
            <Link
              to={`/applications/${appId}/runs/${state.run_id}`}
              className="underline-offset-2 hover:underline"
            >
              this run
            </Link>
            {state.recorded_at && (
              <> on {new Date(state.recorded_at).toLocaleString()}</>
            )}
          </p>
        )}
      </div>
      <p className="mb-3 mt-1 text-xs text-neutral-400">
        What this component manages, as of its last successful apply.
      </p>

      {managedBackend && state?.managed_state && (
        <ManagedStateLine
          appId={appId}
          componentId={componentId}
          version={state.managed_state}
          canDownload={canEdit}
        />
      )}
      {state?.drift && <DriftLine appId={appId} drift={state.drift} />}
      {state?.lock && (
        <LockBox
          appId={appId}
          lock={state.lock}
          onRelease={
            canEdit && onReleaseLock
              ? () => onReleaseLock(state.lock!.id)
              : undefined
          }
        />
      )}

      {error ? (
        <p className="text-sm text-red-400">{error}</p>
      ) : empty ? (
        <p className="text-sm text-neutral-400">
          Nothing recorded yet — deploy this component successfully to see the
          resources it manages and its outputs here.
        </p>
      ) : !state ? (
        <p className="text-sm text-neutral-400">Loading…</p>
      ) : !state.run_id ? (
        // A state view with no recorded apply: it exists only to carry the
        // lock box above.
        <p className="text-sm text-neutral-400">
          Nothing recorded yet — deploy this component successfully to see the
          resources it manages and its outputs here.
        </p>
      ) : (
        <>
          <div className="mb-3 flex items-center gap-4 border-b border-neutral-800">
            <StateTab
              active={tab === "resources"}
              onClick={() => setTab("resources")}
            >
              Resources
              <span className="text-xs text-neutral-500">
                {state.resources.length}
              </span>
            </StateTab>
            <StateTab
              active={tab === "outputs"}
              onClick={() => setTab("outputs")}
            >
              Outputs
              <span className="text-xs text-neutral-500">
                {outputEntries.length}
              </span>
            </StateTab>
          </div>
          {tab === "resources" ? (
            <div className="flex max-h-[32rem] flex-col">
              <ResourcesTable resources={state.resources} />
            </div>
          ) : outputEntries.length > 0 ? (
            <div className="flex max-h-[32rem] flex-col">
              <OutputsTable entries={outputEntries} />
            </div>
          ) : (
            <p className="text-sm text-neutral-400">
              This module declares no outputs.
            </p>
          )}
        </>
      )}
    </div>
  );
}

// LockBox says the state is locked: the component's latest run could not
// acquire the state lock, so nothing will get through until it is released.
// It shows what OpenTofu recorded about the lock — who took it and when, so
// the user can judge whether that run is really dead — and, for editors,
// prefills the force-unlock operation with the lock id (the part people
// otherwise copy out of the logs by hand).
function LockBox({
  appId,
  lock,
  onRelease,
}: {
  appId: string;
  lock: StateLock;
  onRelease?: () => void;
}) {
  return (
    <div className="mb-3 border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-200">
      <p>
        <span className="font-medium">State is locked.</span>{" "}
        <Link
          to={`/applications/${appId}/runs/${lock.run_id}`}
          className="underline-offset-2 hover:underline"
        >
          {lock.failed_at
            ? `The run on ${new Date(lock.failed_at).toLocaleString()}`
            : "The latest run"}
        </Link>{" "}
        could not acquire the state lock, so no plan, apply, or drift check
        will get through until it is released.
      </p>
      <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-xs">
        <dt className="font-sans text-red-300">Lock id</dt>
        <dd className="break-all">{lock.id}</dd>
        {lock.who && (
          <>
            <dt className="font-sans text-red-300">Held by</dt>
            <dd className="break-all">{lock.who}</dd>
          </>
        )}
        {lock.created && (
          <>
            <dt className="font-sans text-red-300">Since</dt>
            <dd>{lock.created}</dd>
          </>
        )}
        {lock.operation && (
          <>
            <dt className="font-sans text-red-300">Operation</dt>
            <dd>{lock.operation}</dd>
          </>
        )}
      </dl>
      {onRelease && (
        <p className="mt-2">
          <button
            type="button"
            onClick={onRelease}
            className="border border-red-500/40 bg-neutral-900 px-3 py-1 text-sm text-red-300 hover:bg-red-500/15"
          >
            Release this lock…
          </button>{" "}
          <span className="text-xs text-red-300">
            fills in the force-unlock operation above; it still waits for
            approval. Only release a lock whose run is definitely no longer
            running.
          </span>
        </p>
      )}
    </div>
  );
}

// ManagedStateLine names the current version of the component's managed
// state and, for editors, offers it as a download — the way to take the
// resources elsewhere (another backend, or out of Spacefleet). The file holds
// every secret the module touched, hence editors only.
function ManagedStateLine({
  appId,
  componentId,
  version,
  canDownload,
}: {
  appId: string;
  componentId: string;
  version: ManagedStateVersion;
  canDownload: boolean;
}) {
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const download = async () => {
    setDownloading(true);
    setError(null);
    const err = await downloadComponentState(appId, componentId);
    setDownloading(false);
    setError(err);
  };
  return (
    <div className="mb-3 flex flex-wrap items-center justify-between gap-2 border border-neutral-800 bg-neutral-800/50 px-3 py-2 text-sm text-neutral-300">
      <p>
        <span className="font-medium text-neutral-100">Managed state</span>{" "}
        version {version.version}, written{" "}
        {version.run_id ? (
          <Link
            to={`/applications/${appId}/runs/${version.run_id}`}
            className="underline-offset-2 hover:underline"
          >
            {new Date(version.written_at).toLocaleString()}
          </Link>
        ) : (
          new Date(version.written_at).toLocaleString()
        )}
      </p>
      {canDownload && (
        <button
          type="button"
          onClick={() => void download()}
          disabled={downloading}
          className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
        >
          <Download className="h-3.5 w-3.5" />
          {downloading ? "Downloading…" : "Download state"}
        </button>
      )}
      {error && <p className="w-full text-xs text-red-400">{error}</p>}
    </div>
  );
}

// DriftLine is the one-line verdict of the component's latest drift check:
// clean, drifted (with the count and the addresses), or a check that itself
// failed (verdict unknown) — each linking to the check's run.
function DriftLine({ appId, drift }: { appId: string; drift: DriftStatus }) {
  const runLink = (
    <Link
      to={`/applications/${appId}/runs/${drift.run_id}`}
      className="underline-offset-2 hover:underline"
    >
      {drift.checked_at
        ? new Date(drift.checked_at).toLocaleString()
        : "the latest check"}
    </Link>
  );
  if (drift.status === "failed") {
    return (
      <p className="mb-3 border border-neutral-800 bg-neutral-800/50 px-3 py-2 text-sm text-neutral-300">
        <span className="font-medium">Drift check failed</span> on {runLink}{" "}
        — the verdict is unknown until a check completes.
      </p>
    );
  }
  if (!drift.has_drift) {
    return (
      <p className="mb-3 border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-200">
        <span className="font-medium">No drift</span> as of {runLink}.
      </p>
    );
  }
  const drifted = drift.drift ?? [];
  return (
    <div className="mb-3 border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
      <p>
        <span className="font-medium">
          Drift detected: {drifted.length} resource
          {drifted.length === 1 ? "" : "s"}
        </span>{" "}
        changed outside of OpenTofu as of {runLink}.
      </p>
      {drifted.length > 0 && (
        <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-xs">
          {drifted.map((r) => (
            <li key={r.address}>
              {r.action === "drift_delete" ? "- " : "~ "}
              {r.address}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function StateTab({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-2 border-b-2 py-2 text-sm ${
        active
          ? "border-white font-medium text-neutral-100"
          : "border-transparent text-neutral-400 hover:text-neutral-100"
      }`}
    >
      {children}
    </button>
  );
}
