import { ChevronRight, RefreshCw, ShieldCheck } from "lucide-react";
import type { components } from "../../api/schema";
import { TypeBadge } from "./TypeBadge";
import { ComponentStatusIcon } from "./status";
import { componentStatusClasses } from "./statusClasses";
import {
  componentSummary,
  type StageViewComponent,
  type StageViewStage,
} from "./stageView";

type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// ComponentCardContent is the inside of a component card: name, the gate and
// continue-on-failure indicators (or, in a run's colors, its status icon), the
// type badge, and the target summary. The builder and the read-only overview
// both wrap it, so a component looks the same in either place.
export function ComponentCardContent({
  component,
  summary,
  status,
  isNew,
}: {
  component: StageViewComponent;
  summary: string;
  status?: ComponentRunStatus;
  isNew?: boolean;
}) {
  return (
    <>
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium text-neutral-900">
          {component.name || "(unnamed)"}
        </span>
        <span className="flex shrink-0 items-center gap-1.5">
          {isNew && (
            <span className="text-[10px] uppercase tracking-wide text-neutral-400">
              new
            </span>
          )}
          {component.requires_approval && (
            <span title="Requires approval: the run waits for a person before this runs">
              <ShieldCheck
                aria-label="Requires approval"
                className="h-3.5 w-3.5 text-violet-600"
              />
            </span>
          )}
          {component.continue_on_failure && (
            <span title="Continue on failure: a failure here won't stop the later stages">
              <RefreshCw
                aria-label="Continues on failure"
                className="h-3.5 w-3.5 text-amber-600"
              />
            </span>
          )}
          {status && <ComponentStatusIcon status={status} />}
        </span>
      </div>
      <div className="mt-1.5 flex min-w-0 items-center gap-2">
        <TypeBadge type={component.type} />
        {summary && (
          <span className="truncate text-xs text-neutral-500" title={summary}>
            {summary}
          </span>
        )}
      </div>
    </>
  );
}

// StageConnector is the arrow between two stage columns: run order flows left
// to right.
export function StageConnector() {
  return (
    <div
      aria-hidden="true"
      className="flex w-6 shrink-0 items-start justify-center pt-4 text-neutral-300"
    >
      <ChevronRight className="h-4 w-4" />
    </div>
  );
}

// StageColumnsView is the read-only stage layout — the application page's
// at-a-glance view of what a deploy does. Columns run left to right; each
// card can be colored by a status (the latest run's, keyed by component id).
// Clicking a card calls onOpen with the component.
export function StageColumnsView({
  stages,
  clusterName,
  statusByComponent,
  onOpen,
}: {
  stages: StageViewStage[];
  clusterName: (id: string) => string | undefined;
  statusByComponent?: Record<string, ComponentRunStatus>;
  onOpen?: (component: StageViewComponent) => void;
}) {
  return (
    <div className="overflow-x-auto">
      <div className="flex w-max min-w-full items-stretch p-4">
        {stages.map((st, i) => (
          <div key={st.id} className="flex items-stretch">
            {i > 0 && <StageConnector />}
            <section
              aria-label={`Stage ${st.name}`}
              className="flex w-60 shrink-0 flex-col border border-neutral-200 bg-neutral-50"
            >
              <header className="border-b border-neutral-200 px-3 py-2">
                <p className="text-[10px] font-medium uppercase tracking-wide text-neutral-400">
                  Stage {i + 1}
                </p>
                <h3 className="truncate text-sm font-semibold text-neutral-900">
                  {st.name}
                </h3>
              </header>
              <div className="flex flex-1 flex-col gap-2 p-2">
                {st.components.length === 0 ? (
                  <p className="px-1 py-2 text-xs text-neutral-400">
                    No components.
                  </p>
                ) : (
                  st.components.map((c) => {
                    const status = statusByComponent?.[c.id];
                    return (
                      <button
                        key={c.id}
                        type="button"
                        onClick={() => onOpen?.(c)}
                        className={`border px-3 py-2 text-left hover:border-neutral-500 ${
                          status
                            ? componentStatusClasses(status)
                            : "border-neutral-300 bg-white"
                        }`}
                      >
                        <ComponentCardContent
                          component={c}
                          summary={componentSummary(c, clusterName)}
                          status={status}
                        />
                      </button>
                    );
                  })
                )}
              </div>
            </section>
          </div>
        ))}
      </div>
    </div>
  );
}
