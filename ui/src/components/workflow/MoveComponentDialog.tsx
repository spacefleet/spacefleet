import { useEffect, useState, type FormEvent } from "react";
import { ArrowRightLeft, Loader2, X } from "lucide-react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";

type Application = components["schemas"]["Application"];
type WorkflowStage = components["schemas"]["WorkflowStage"];

// The stage select's value for "add a stage at the end".
const NEW_STAGE = "__new__";

// MoveComponentDialog moves a component to a stage of another application —
// or another stage of its own (the component page has no drag) — keeping its
// settings, variables, and managed state. The server does the move; the
// caller saves the draft first (onBeforeMove) so nothing pending is lost,
// and is handed the destination on success.
export function MoveComponentDialog({
  appId,
  component,
  onClose,
  onBeforeMove,
  onMoved,
}: {
  appId: string;
  component: { id: string; name: string };
  onClose: () => void;
  // Saves pending edits; resolves to an error message to stop the move.
  onBeforeMove: () => Promise<string | null>;
  onMoved: (applicationId: string) => void;
}) {
  const [apps, setApps] = useState<Application[] | null>(null);
  const [target, setTarget] = useState(appId);
  const [stages, setStages] = useState<WorkflowStage[] | null>(null);
  const [stageId, setStageId] = useState("");
  const [newStageName, setNewStageName] = useState("");
  const [name, setName] = useState(component.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      const { data, error } = await api.GET("/api/applications");
      if (error || !data) {
        setError(error?.message ?? "Could not load the applications");
        return;
      }
      setApps([...data].sort((a, b) => a.name.localeCompare(b.name)));
    })();
  }, []);

  // The destination's stages, and a fresh default for a new stage's name.
  useEffect(() => {
    let cancelled = false;
    setStages(null);
    void (async () => {
      const { data, error } = await api.GET("/api/applications/{id}/workflow", {
        params: { path: { id: target } },
      });
      if (cancelled) return;
      if (error || !data) {
        setError(error?.message ?? "Could not load that application's workflow");
        return;
      }
      setStages(data.stages);
      setStageId(data.stages.length > 0 ? data.stages[0].id : NEW_STAGE);
      setNewStageName(`Stage ${data.stages.length + 1}`);
    })();
    return () => {
      cancelled = true;
    };
  }, [target]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, busy]);

  const trimmed = name.trim();
  const nameTaken =
    stages?.some((st) =>
      st.components.some((c) => c.id !== component.id && c.name === trimmed),
    ) ?? false;
  const sameApp = target === appId;
  const ownStage = stages?.find((st) =>
    st.components.some((c) => c.id === component.id),
  );
  const noChange =
    sameApp && stageId === ownStage?.id && trimmed === component.name;
  const ready =
    stages !== null &&
    trimmed !== "" &&
    !nameTaken &&
    !noChange &&
    (stageId !== NEW_STAGE || newStageName.trim() !== "");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!ready) return;
    setBusy(true);
    setError(null);
    const saveErr = await onBeforeMove();
    if (saveErr) {
      setError(`Save the workflow first: ${saveErr}`);
      setBusy(false);
      return;
    }
    const { error } = await api.POST(
      "/api/applications/{id}/components/{componentId}/move",
      {
        params: { path: { id: appId, componentId: component.id } },
        body: {
          application_id: target,
          ...(stageId === NEW_STAGE
            ? { new_stage_name: newStageName.trim() }
            : { stage_id: stageId }),
          ...(trimmed !== component.name ? { name: trimmed } : {}),
        },
      },
    );
    if (error) {
      setError(error.message ?? "Could not move the component");
      setBusy(false);
      return;
    }
    onMoved(target);
  }

  const targetName = apps?.find((a) => a.id === target)?.name ?? "";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <form
        role="dialog"
        aria-modal="true"
        aria-labelledby="move-component-title"
        onSubmit={(e) => void submit(e)}
        className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2
            id="move-component-title"
            className="inline-flex min-w-0 items-center gap-2 text-lg font-semibold tracking-tight"
          >
            <ArrowRightLeft className="h-5 w-5 shrink-0 text-neutral-400" />
            <span className="truncate">Move {component.name}</span>
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

        <div className="space-y-4 px-5 py-4 text-sm">
          <label className="flex flex-col gap-1 text-neutral-300">
            Application
            <select
              value={target}
              onChange={(e) => {
                setTarget(e.target.value);
                setError(null);
              }}
              disabled={busy || apps === null}
              className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm text-neutral-100"
            >
              {apps === null && <option value={appId}>Loading…</option>}
              {apps?.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                  {a.id === appId ? " (this application)" : ""}
                </option>
              ))}
            </select>
          </label>

          <label className="flex flex-col gap-1 text-neutral-300">
            Stage
            <select
              value={stageId}
              onChange={(e) => setStageId(e.target.value)}
              disabled={busy || stages === null}
              className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm text-neutral-100"
            >
              {stages === null && <option value="">Loading…</option>}
              {stages?.map((st, i) => (
                <option key={st.id} value={st.id}>
                  {i + 1}. {st.name}
                  {st.id === ownStage?.id ? " (where it is now)" : ""}
                </option>
              ))}
              {stages !== null && (
                <option value={NEW_STAGE}>New stage at the end</option>
              )}
            </select>
          </label>
          {stageId === NEW_STAGE && (
            <label className="flex flex-col gap-1 text-neutral-300">
              New stage name
              <input
                type="text"
                value={newStageName}
                onChange={(e) => setNewStageName(e.target.value)}
                disabled={busy}
                className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm text-neutral-100"
              />
            </label>
          )}

          <div className="flex flex-col gap-1">
            <label className="flex flex-col gap-1 text-neutral-300">
              Name
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={busy}
                aria-invalid={nameTaken}
                className="w-full border border-neutral-700 bg-neutral-900 px-3 py-2 font-mono text-sm text-neutral-100"
              />
            </label>
            {nameTaken && (
              <span className="text-xs text-red-400">
                {targetName || "That application"} already has a component
                named {trimmed} — pick another name.
              </span>
            )}
          </div>

          {!sameApp && (
            <ul className="list-disc space-y-1 pl-5 text-xs text-neutral-400">
              <li>
                Its settings, variables, and state go with it. Its run history
                stays with this application.
              </li>
              <li>
                Its jobs will run on {targetName || "that application"}&apos;s
                runner cluster, so cluster access and credentials may differ.
              </li>
              <li>
                References aren&apos;t checked: a{" "}
                <code className="font-mono">{"${{ components.* }}"}</code>{" "}
                reference to or from it that no longer resolves fails that
                workflow&apos;s next save or run, for you to fix then.
              </li>
            </ul>
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
            type="submit"
            disabled={busy || !ready}
            className="inline-flex items-center gap-1.5 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
          >
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {busy ? "Moving…" : "Move component"}
          </button>
        </div>
      </form>
    </div>
  );
}
