import { useState } from "react";
import { ChevronDown, ChevronRight, Loader2, Play, X } from "lucide-react";
import { api } from "../../api/client";
import { runStartError } from "./runAction";

// parseTargets splits the targets field — one resource address per line, or
// comma-separated — into the list the API validates.
function parseTargets(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((t) => t.trim())
    .filter((t) => t !== "");
}

// ComponentRunDialog starts a deploy of one OpenTofu component on its own —
// its plan and apply, without the rest of the workflow — with the per-run
// options gathered first: the git ref to run in place of the component's own,
// and, tucked under Advanced, resource addresses to limit the plan to (each
// becomes a -target; an escape hatch, not a routine run). The component's
// approval gate and policy still apply. The caller's pending edits are saved
// first (onBeforeRun) so the run sees them; on success it hands the new run's
// id back so the caller can open its live view.
export function ComponentRunDialog({
  appId,
  component,
  onClose,
  onBeforeRun,
  onStarted,
}: {
  appId: string;
  component: { id: string; name: string; config: Record<string, string> };
  onClose: () => void;
  // Saves pending edits; resolves to an error message to stop the run.
  onBeforeRun: () => Promise<string | null>;
  onStarted: (runId: string) => void;
}) {
  const [gitRef, setGitRef] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [targetsRaw, setTargetsRaw] = useState("");
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ownRef = component.config.git_ref ?? "";
  const targets = parseTargets(targetsRaw);

  async function start() {
    setStarting(true);
    setError(null);
    const saveError = await onBeforeRun();
    if (saveError) {
      setError(saveError);
      setStarting(false);
      return;
    }
    const ref = gitRef.trim();
    const { data, error, response } = await api.POST(
      "/api/applications/{id}/components/{componentId}/runs",
      {
        params: { path: { id: appId, componentId: component.id } },
        body: {
          action: "deploy",
          ...(ref ? { git_ref: ref } : {}),
          ...(targets.length > 0 ? { targets } : {}),
        },
      },
    );
    if (error || !data) {
      setError(runStartError(error, response?.status));
      setStarting(false);
      return;
    }
    onStarted(data.id);
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="component-run-dialog-title"
        className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2
            id="component-run-dialog-title"
            className="break-all text-lg font-semibold tracking-tight"
          >
            Run {component.name}
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={starting}
            className="text-neutral-500 hover:text-neutral-300 disabled:opacity-50"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <p className="text-sm text-neutral-300">
            Plans and applies just this component, without the rest of the
            workflow. Its approval gate and policies still apply.
          </p>

          <div className="flex flex-col gap-1 text-sm text-neutral-300">
            <label htmlFor="component-run-git-ref">Git ref</label>
            <input
              id="component-run-git-ref"
              type="text"
              aria-describedby="component-run-git-ref-help"
              value={gitRef}
              onChange={(e) => {
                setGitRef(e.target.value);
                setError(null);
              }}
              disabled={starting}
              placeholder={ownRef || "default branch"}
              className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans placeholder:text-neutral-500"
            />
            <p
              id="component-run-git-ref-help"
              className="text-xs text-neutral-400"
            >
              A branch, tag, or full commit SHA to run instead of{" "}
              {ownRef ? (
                <code className="font-mono text-neutral-300">{ownRef}</code>
              ) : (
                "the repository's default branch"
              )}
              . Leave it blank to use that.
            </p>
          </div>

          <div>
            <button
              type="button"
              onClick={() => setAdvanced((o) => !o)}
              aria-expanded={advanced}
              className="inline-flex items-center gap-1.5 text-sm text-neutral-300 hover:text-neutral-100"
            >
              {advanced ? (
                <ChevronDown className="h-4 w-4 text-neutral-500" />
              ) : (
                <ChevronRight className="h-4 w-4 text-neutral-500" />
              )}
              Advanced
              {!advanced && targets.length > 0 && (
                <span className="text-xs text-neutral-400">
                  {targets.length} target{targets.length === 1 ? "" : "s"}
                </span>
              )}
            </button>
            {advanced && (
              <label className="mt-3 flex flex-col gap-1 text-sm text-neutral-300">
                Limit to resources
                <textarea
                  aria-label="Target addresses"
                  value={targetsRaw}
                  rows={3}
                  placeholder={"aws_instance.web\nmodule.vpc.aws_subnet.private[0]"}
                  onChange={(e) => {
                    setTargetsRaw(e.target.value);
                    setError(null);
                  }}
                  disabled={starting}
                  className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 font-mono text-sm text-neutral-100 placeholder:font-sans placeholder:text-neutral-500"
                />
                <span className="text-xs text-neutral-400">
                  One address per line; each becomes a{" "}
                  <code className="font-mono">-target</code> on the plan. For
                  exceptional cases like recovering from an error: a targeted
                  apply leaves the rest of the module unreconciled, so follow
                  it with a full run.
                </span>
              </label>
            )}
          </div>

          {error && <p className="text-sm text-red-400">{error}</p>}
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-neutral-800 px-5 py-4">
          <button
            type="button"
            onClick={onClose}
            disabled={starting}
            className="text-sm text-neutral-400 hover:text-neutral-100 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void start()}
            disabled={starting}
            className="inline-flex items-center gap-1.5 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
          >
            {starting ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Play className="h-4 w-4" />
            )}
            {starting ? "Starting…" : "Run"}
          </button>
        </div>
      </div>
    </div>
  );
}
