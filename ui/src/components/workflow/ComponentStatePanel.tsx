import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
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
type StateOperationKind = components["schemas"]["StateOperationKind"];

// A prefilled operation — what the lock box hands the Operations form when
// the user clicks "Release this lock".
type OperationPrefill = { kind: StateOperationKind; values: Record<string, string> };

// ComponentStatePanel is an OpenTofu component's persistent "what do I own"
// view: the outputs and the managed-resource inventory its last successful
// apply recorded, with a link to the run that recorded them. It reads the
// component-state endpoint; a component that has never applied successfully
// (404) shows a quiet placeholder rather than an error.
export function ComponentStatePanel({
  appId,
  componentId,
  canEdit = false,
  managedBackend = false,
}: {
  appId: string;
  componentId: string;
  // Editor or above: shows the guarded state operations (each starts an
  // approval-gated run) and the state download. Viewers see the recorded
  // state only.
  canEdit?: boolean;
  // The component keeps its state in Spacefleet (the managed backend), so
  // its current state version is shown, and editors can download it. A
  // cloud backend's state is in its own bucket.
  managedBackend?: boolean;
}) {
  const [state, setState] = useState<ComponentState | null>(null);
  const [empty, setEmpty] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<"resources" | "outputs">("resources");
  const [prefill, setPrefill] = useState<OperationPrefill | null>(null);

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
            canEdit
              ? () => setPrefill({ kind: "force_unlock", values: { lock_id: state.lock!.id } })
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

      {canEdit && (
        <StateOperations appId={appId} componentId={componentId} prefill={prefill} />
      )}
      {canEdit && <ScopedRuns appId={appId} componentId={componentId} />}
    </div>
  );
}

// parseTargets splits the targets field — one resource address per line, or
// comma-separated — into the list the API validates.
function parseTargets(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((t) => t.trim())
    .filter((t) => t !== "");
}

// ScopedRuns is the editor-only "Destroy and targeted runs" section: a run
// limited to this component alone — a deploy of just this module, or its
// destruction — optionally narrowed to a fixed list of resource addresses
// (each becomes a -target flag on the plan; never free-form flags). A
// destroy asks for confirmation here and then always parks its apply for
// approval, so the destroy plan is reviewed before anything goes. On
// success the user is taken straight to the run.
function ScopedRuns({
  appId,
  componentId,
}: {
  appId: string;
  componentId: string;
}) {
  const navigate = useNavigate();
  const [targetsRaw, setTargetsRaw] = useState("");
  const [confirmDestroy, setConfirmDestroy] = useState(false);
  const [submitting, setSubmitting] = useState<"deploy" | "uninstall" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const targets = parseTargets(targetsRaw);

  const start = async (action: "deploy" | "uninstall") => {
    setSubmitting(action);
    setError(null);
    const { data, error } = await api.POST(
      "/api/applications/{id}/components/{componentId}/runs",
      {
        params: { path: { id: appId, componentId } },
        body: targets.length > 0 ? { action, targets } : { action },
      },
    );
    setSubmitting(null);
    if (error || !data) {
      setError(error?.message ?? "Could not start the run");
      return;
    }
    navigate(`/applications/${appId}/runs/${data.id}`);
  };

  return (
    <div className="mt-6 border-t border-neutral-800 pt-4">
      <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
        Destroy and targeted runs
      </h3>
      <p className="mb-3 mt-1 text-xs text-neutral-400">
        Run just this component, without the rest of the workflow. List
        resource addresses to limit the run to those resources (each becomes
        a <code className="font-mono">-target</code>); leave it empty to
        cover the whole component.
      </p>
      <label className="flex flex-col gap-1 text-xs text-neutral-300">
        Targets (optional, one per line)
        <textarea
          aria-label="Target addresses"
          value={targetsRaw}
          rows={2}
          placeholder={"aws_instance.web\nmodule.vpc.aws_subnet.private[0]"}
          onChange={(e) => {
            setTargetsRaw(e.target.value);
            setError(null);
          }}
          className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans placeholder:text-neutral-500"
        />
      </label>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => void start("deploy")}
          disabled={submitting !== null}
          className="bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
        >
          {submitting === "deploy"
            ? "Starting…"
            : targets.length > 0
              ? "Deploy targets"
              : "Deploy this component"}
        </button>
        {!confirmDestroy && (
          <button
            type="button"
            onClick={() => {
              setConfirmDestroy(true);
              setError(null);
            }}
            disabled={submitting !== null}
            className="border border-red-500/40 px-3 py-1.5 text-sm text-red-300 hover:bg-red-500/10 disabled:opacity-50"
          >
            {targets.length > 0 ? "Destroy targets…" : "Destroy this component…"}
          </button>
        )}
      </div>
      {confirmDestroy && (
        <div className="mt-3 border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-200">
          <p>
            {targets.length > 0
              ? `This plans the destruction of ${targets.length} targeted resource${targets.length === 1 ? "" : "s"}.`
              : "This plans the destruction of every resource this component manages."}{" "}
            The destroy always waits for approval: review the plan on the run,
            then approve to destroy or reject to keep everything.
          </p>
          <div className="mt-2 flex items-center gap-2">
            <button
              type="button"
              onClick={() => void start("uninstall")}
              disabled={submitting !== null}
              className="bg-red-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-800 disabled:opacity-50"
            >
              {submitting === "uninstall" ? "Starting…" : "Start destroy for approval"}
            </button>
            <button
              type="button"
              onClick={() => setConfirmDestroy(false)}
              disabled={submitting !== null}
              className="px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-900 disabled:opacity-50"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
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
  prefill,
}: {
  appId: string;
  componentId: string;
  // Set by the lock box: selects the operation and fills its fields (each
  // new prefill object re-applies, so clicking "Release" twice works).
  prefill?: OperationPrefill | null;
}) {
  const navigate = useNavigate();
  const [kind, setKind] = useState<StateOperationKind>("rm");
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!prefill) return;
    setKind(prefill.kind);
    setValues(prefill.values);
    setError(null);
  }, [prefill]);
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
    <form onSubmit={(e) => void submit(e)} className="mt-6 border-t border-neutral-800 pt-4">
      <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
        Operations
      </h3>
      <p className="mb-3 mt-1 text-xs text-neutral-400">
        Guarded state operations. Each starts a run that waits for approval,
        showing the exact command before it touches state, and refreshes the
        recorded state afterwards.
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-neutral-300">
          Operation
          <select
            aria-label="State operation"
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as StateOperationKind);
              setValues({});
              setError(null);
            }}
            className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
          >
            {STATE_OPS.map((o) => (
              <option key={o.kind} value={o.kind}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        {op.fields.map((f) => (
          <label key={f.name} className="flex min-w-[16rem] flex-1 flex-col gap-1 text-xs text-neutral-300">
            {f.label}
            <input
              type="text"
              aria-label={f.label}
              value={values[f.name] ?? ""}
              placeholder={f.placeholder}
              onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
              className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans placeholder:text-neutral-500"
            />
          </label>
        ))}
        <button
          type="submit"
          disabled={!complete || submitting}
          className="bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
        >
          {submitting ? "Starting…" : "Start for approval"}
        </button>
      </div>
      <p className="mt-2 text-xs text-neutral-400">{op.hint}</p>
      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
    </form>
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
            fills in the force-unlock operation below; it still waits for
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
