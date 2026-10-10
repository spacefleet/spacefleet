import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { api } from "../../api/client";
import type { components } from "../../api/schema";

type StateOperationKind = components["schemas"]["StateOperationKind"];

// A prefilled operation — what the State card's lock box hands this card
// when the user clicks "Release this lock".
export type OperationPrefill = {
  kind: StateOperationKind;
  values: Record<string, string>;
};

// The fixed menu of guarded state operations, each with the command it runs
// and the fields it takes.
const STATE_OPS: {
  kind: StateOperationKind;
  label: string;
  command: string;
  hint: string;
  fields: {
    name: "address" | "new_address" | "lock_id" | "import_id";
    label: string;
    placeholder: string;
  }[];
}[] = [
  {
    kind: "rm",
    label: "Stop managing a resource",
    command: "tofu state rm",
    hint: "Forgets the resource in state. The real infrastructure is not destroyed; OpenTofu simply stops managing it.",
    fields: [{ name: "address", label: "Resource address", placeholder: "aws_instance.web" }],
  },
  {
    kind: "mv",
    label: "Rename a resource",
    command: "tofu state mv",
    hint: "Moves a resource to a new address in state, so a refactor (a rename, a move into a module) is not a destroy and create.",
    fields: [
      { name: "address", label: "Current address", placeholder: "aws_instance.web" },
      { name: "new_address", label: "New address", placeholder: "module.web.aws_instance.this" },
    ],
  },
  {
    kind: "import",
    label: "Import existing infrastructure",
    command: "tofu import",
    hint: "Adopts a resource that already exists into state, under the given address. The module must already declare that address.",
    fields: [
      { name: "address", label: "Resource address", placeholder: "aws_s3_bucket.data" },
      { name: "import_id", label: "Import id", placeholder: "the provider's id, e.g. a bucket name or an instance id" },
    ],
  },
  {
    kind: "force_unlock",
    label: "Release a stuck state lock",
    command: "tofu force-unlock",
    hint: "Releases a lock left behind by a run that did not finish. Only do this when you are sure no other run is still using the state.",
    fields: [{ name: "lock_id", label: "Lock id", placeholder: "from the \"Error acquiring the state lock\" message" }],
  },
];

// ComponentOperations is an OpenTofu component's editor-only Operations
// card, in two steps: pick one of the four guarded operations from a button
// group, then read what it does, fill in its typed fields, and start it (or
// go back). The request opens a state_op run parked at its approval gate —
// nothing touches state until someone reviews the exact command on the run
// and approves it — so on success the user is taken straight to that run.
export function ComponentOperations({
  appId,
  componentId,
  prefill,
}: {
  appId: string;
  componentId: string;
  // Set from the State card's lock box: opens the operation with its fields
  // filled and brings the card into view (each new prefill object re-applies,
  // so clicking "Release" twice works).
  prefill?: OperationPrefill | null;
}) {
  const navigate = useNavigate();
  const cardRef = useRef<HTMLFormElement>(null);
  // null is the first step: no operation chosen yet.
  const [kind, setKind] = useState<StateOperationKind | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!prefill) return;
    setKind(prefill.kind);
    setValues(prefill.values);
    setError(null);
    cardRef.current?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  }, [prefill]);
  const op = STATE_OPS.find((o) => o.kind === kind) ?? null;
  const complete =
    op !== null && op.fields.every((f) => (values[f.name] ?? "").trim() !== "");

  const choose = (next: StateOperationKind | null) => {
    setKind(next);
    setValues({});
    setError(null);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!op || !complete) return;
    setSubmitting(true);
    setError(null);
    const body: Record<string, string> = { operation: op.kind };
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
    <form
      ref={cardRef}
      onSubmit={(e) => void submit(e)}
      className="mt-6 scroll-mt-6 border border-neutral-800 bg-neutral-900 p-4"
    >
      <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
        Operations
      </h2>
      <p className="mb-3 mt-1 text-xs text-neutral-400">
        Guarded state operations. Each starts a run that waits for approval,
        showing the exact command before it touches state, and refreshes the
        recorded state afterwards.
      </p>

      {!op ? (
        <div
          role="group"
          aria-label="State operation"
          className="grid grid-cols-1 gap-2 sm:grid-cols-2"
        >
          {STATE_OPS.map((o) => (
            <button
              key={o.kind}
              type="button"
              onClick={() => choose(o.kind)}
              className="flex flex-col items-start gap-0.5 border border-neutral-700 px-3 py-2 text-left hover:bg-neutral-800"
            >
              <span className="text-sm font-medium text-neutral-100">{o.label}</span>
              <span className="font-mono text-xs text-neutral-400">{o.command}</span>
            </button>
          ))}
        </div>
      ) : (
        <div className="border border-neutral-800 bg-neutral-800/50 p-3">
          <p className="text-sm font-medium text-neutral-100">
            {op.label}{" "}
            <span className="ml-1 font-mono text-xs font-normal text-neutral-400">
              {op.command}
            </span>
          </p>
          <p className="mt-1 text-xs text-neutral-300">{op.hint}</p>
          <div className="mt-3 flex flex-col gap-3">
            {op.fields.map((f) => (
              <label key={f.name} className="flex flex-col gap-1 text-xs text-neutral-300">
                {f.label}
                <input
                  type="text"
                  aria-label={f.label}
                  value={values[f.name] ?? ""}
                  placeholder={f.placeholder}
                  onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
                  disabled={submitting}
                  className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans placeholder:text-neutral-500"
                />
              </label>
            ))}
          </div>
          <div className="mt-3 flex items-center gap-3">
            <button
              type="submit"
              disabled={!complete || submitting}
              className="bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
            >
              {submitting ? "Starting…" : "Start for approval"}
            </button>
            <button
              type="button"
              onClick={() => choose(null)}
              disabled={submitting}
              className="text-sm text-neutral-400 hover:text-neutral-100 disabled:opacity-50"
            >
              Back
            </button>
          </div>
          {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
        </div>
      )}
    </form>
  );
}
