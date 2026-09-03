import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { OutputsTable } from "./OutputsTable";
import { ResourcesTable } from "./ResourcesTable";

type ComponentState = components["schemas"]["ComponentState"];
type DriftStatus = components["schemas"]["DriftStatus"];

// ComponentStatePanel is an OpenTofu component's persistent "what do I own"
// view: the outputs and the managed-resource inventory its last successful
// apply recorded, with a link to the run that recorded them. It reads the
// component-state endpoint; a component that has never applied successfully
// (404) shows a quiet placeholder rather than an error.
export function ComponentStatePanel({
  appId,
  componentId,
}: {
  appId: string;
  componentId: string;
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
    <div className="mt-6 border border-neutral-200 bg-white p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
          State
        </h2>
        {state && (
          <p className="text-xs text-neutral-500">
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
      <p className="mb-3 mt-1 text-xs text-neutral-500">
        What this component manages, as of its last successful apply.
      </p>

      {state?.drift && <DriftLine appId={appId} drift={state.drift} />}

      {error ? (
        <p className="text-sm text-red-600">{error}</p>
      ) : empty ? (
        <p className="text-sm text-neutral-500">
          Nothing recorded yet — deploy this component successfully to see the
          resources it manages and its outputs here.
        </p>
      ) : !state ? (
        <p className="text-sm text-neutral-500">Loading…</p>
      ) : (
        <>
          <div className="mb-3 flex items-center gap-4 border-b border-neutral-200">
            <StateTab
              active={tab === "resources"}
              onClick={() => setTab("resources")}
            >
              Resources
              <span className="text-xs text-neutral-400">
                {state.resources.length}
              </span>
            </StateTab>
            <StateTab
              active={tab === "outputs"}
              onClick={() => setTab("outputs")}
            >
              Outputs
              <span className="text-xs text-neutral-400">
                {outputEntries.length}
              </span>
            </StateTab>
          </div>
          {tab === "resources" ? (
            <div className="max-h-[32rem]">
              <ResourcesTable resources={state.resources} />
            </div>
          ) : outputEntries.length > 0 ? (
            <div className="max-h-[32rem]">
              <OutputsTable entries={outputEntries} />
            </div>
          ) : (
            <p className="text-sm text-neutral-500">
              This module declares no outputs.
            </p>
          )}
        </>
      )}
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
      <p className="mb-3 border border-neutral-200 bg-neutral-50 px-3 py-2 text-sm text-neutral-700">
        <span className="font-medium">Drift check failed</span> on {runLink}{" "}
        — the verdict is unknown until a check completes.
      </p>
    );
  }
  if (!drift.has_drift) {
    return (
      <p className="mb-3 border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-900">
        <span className="font-medium">No drift</span> as of {runLink}.
      </p>
    );
  }
  const drifted = drift.drift ?? [];
  return (
    <div className="mb-3 border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900">
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
          ? "border-black font-medium text-neutral-900"
          : "border-transparent text-neutral-500 hover:text-neutral-900"
      }`}
    >
      {children}
    </button>
  );
}
