import type { components } from "../../api/schema";
import { componentStatusFill } from "./statusClasses";

type RunStage = components["schemas"]["RunStage"];

// StageBar is a run's shape at a glance: one block per stage, in order,
// colored by the stage's combined status, with the stage name and status on
// hover. It reads the server's stage summary, so a run from before stages had
// existed still shows the stages derived for it.
export function StageBar({ stages }: { stages: RunStage[] | undefined }) {
  if (!stages || stages.length === 0) return null;
  const describe = (st: RunStage) => `${st.name}: ${st.status.replace(/_/g, " ")}`;
  return (
    <span
      role="img"
      aria-label={`Stages — ${stages.map(describe).join(", ")}`}
      className="inline-flex items-center gap-0.5"
    >
      {stages.map((st, i) => (
        <span
          key={`${i}-${st.name}`}
          title={describe(st)}
          className={`h-2 w-5 ${componentStatusFill(st.status)}`}
        />
      ))}
    </span>
  );
}
