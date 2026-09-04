import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { OutputsTable } from "./OutputsTable";
import { ResourcesTable } from "./ResourcesTable";

type ComponentState = components["schemas"]["ComponentState"];
type DriftStatus = components["schemas"]["DriftStatus"];
type StateOperationKind = components["schemas"]["StateOperationKind"];

// ComponentStatePanel is an OpenTofu component's persistent "what do I own"
// view: the outputs and the managed-resource inventory its last successful
// apply recorded, with a link to the run that recorded them. It reads the
// component-state endpoint; a component that has never applied successfully
// (404) shows a quiet placeholder rather than an error.
export function ComponentStatePanel({
  appId,
  componentId,
  canEdit = false,
}: {
  appId: string;
  componentId: string;
  // Editor or above: shows the guarded state operations (each starts an
  // approval-gated run). Viewers see the recorded state only.
  canEdit?: boolean;
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

      {canEdit && <StateOperations appId={appId} componentId={componentId} />}
    </div>
  );
}

// The fixed menu of guarded state operations, each with the fields it takes.
const STATE_OPS: {
  kind: StateOperationKind;
  label: string;
  hint: string;
  fields: { name: "address" | "new_address" | "lock_id" | "import_id"; label: string; placeholder: string }[];
}[] = [
  {
    kind: "rm",
    label: "Stop managing a resource",
    hint: "tofu state rm — forgets the resource in state. The real infrastructure is not destroyed; OpenTofu simply stops managing it.",
    fields: [{ name: "address", label: "Resource address", placeholder: "aws_instance.web" }],
  },
  {
    kind: "mv",
    label: "Rename a resource",
    hint: "tofu state mv — moves a resource to a new address in state, so a refactor (a rename, a move into a module) is not a destroy and create.",
    fields: [
      { name: "address", label: "Current address", placeholder: "aws_instance.web" },
      { name: "new_address", label: "New address", placeholder: "module.web.aws_instance.this" },
    ],
  },
  {
    kind: "import",
    label: "Import existing infrastructure",
    hint: "tofu import — adopts a resource that already exists into state, under the given address. The module must already declare that address.",
    fields: [
      { name: "address", label: "Resource address", placeholder: "aws_s3_bucket.data" },
      { name: "import_id", label: "Import id", placeholder: "the provider's id, e.g. a bucket name or an instance id" },
    ],
  },
  {
    kind: "force_unlock",
    label: "Release a stuck state lock",
    hint: "tofu force-unlock — releases a lock left behind by a run that did not finish. Only do this when you are sure no other run is still using the state.",
    fields: [{ name: "lock_id", label: "Lock id", placeholder: "from the \"Error acquiring the state lock\" message" }],
  },
];

// StateOperations is the editor-only "Operations" section of the panel: pick
// one of the four guarded operations, fill in its typed fields, and start it.
// The request opens a state_op run parked at its approval gate — nothing
// touches state until someone reviews the exact command on the run and
// approves it — so on success the user is taken straight to that run.
function StateOperations({
  appId,
  componentId,
}: {
  appId: string;
  componentId: string;
}) {
  const navigate = useNavigate();
  const [kind, setKind] = useState<StateOperationKind>("rm");
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const op = STATE_OPS.find((o) => o.kind === kind) ?? STATE_OPS[0];
  const complete = op.fields.every((f) => (values[f.name] ?? "").trim() !== "");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    const body: Record<string, string> = { operation: kind };
    for (const f of op.fields) body[f.name] = (values[f.name] ?? "").trim();
    const { data, error } = await api.POST(
      "/api/applications/{id}/components/{componentId}/state-ops",
      {
        params: { path: { id: appId, componentId } },
        body: body as { operation: StateOperationKind },
      },
    );
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not start the operation");
      return;
    }
    navigate(`/applications/${appId}/runs/${data.id}`);
  };

  return (
    <form onSubmit={(e) => void submit(e)} className="mt-6 border-t border-neutral-200 pt-4">
      <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
        Operations
      </h3>
      <p className="mb-3 mt-1 text-xs text-neutral-500">
        Guarded state operations. Each starts a run that waits for approval,
        showing the exact command before it touches state, and refreshes the
        recorded state afterwards.
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-neutral-600">
          Operation
          <select
            aria-label="State operation"
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as StateOperationKind);
              setValues({});
              setError(null);
            }}
            className="border border-neutral-300 bg-white px-2 py-1.5 text-sm text-neutral-900"
          >
            {STATE_OPS.map((o) => (
              <option key={o.kind} value={o.kind}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        {op.fields.map((f) => (
          <label key={f.name} className="flex min-w-[16rem] flex-1 flex-col gap-1 text-xs text-neutral-600">
            {f.label}
            <input
              type="text"
              aria-label={f.label}
              value={values[f.name] ?? ""}
              placeholder={f.placeholder}
              onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
              className="border border-neutral-300 bg-white px-2 py-1.5 font-mono text-sm text-neutral-900 placeholder:font-sans placeholder:text-neutral-400"
            />
          </label>
        ))}
        <button
          type="submit"
          disabled={!complete || submitting}
          className="bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
        >
          {submitting ? "Starting…" : "Start for approval"}
        </button>
      </div>
      <p className="mt-2 text-xs text-neutral-500">{op.hint}</p>
      {error && <p className="mt-2 text-xs text-red-600">{error}</p>}
    </form>
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
