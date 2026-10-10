import { useEffect, useMemo, useState } from "react";
import { api } from "../../api/client";
import { useOrg } from "../../contexts/OrgContext";
import type { components } from "../../api/schema";
import { StageColumnsView } from "./StageColumns";

type Workflow = components["schemas"]["Workflow"];
type RunStage = components["schemas"]["RunStage"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// WorkflowStagesOverview is the read-only at-a-glance workflow on the
// application page: the saved stages and their components, laid out like the
// builder, with each component colored by how it did in the latest run (when
// that run covered it). It answers "what does a deploy do, and how did the last
// one go" without opening the builder; clicking a component opens its page.
export function WorkflowStagesOverview({
  appId,
  clusterName,
  latestStages,
  onOpen,
}: {
  appId: string;
  clusterName: (id: string) => string | undefined;
  // The latest run's stage summary, if there is a latest run.
  latestStages?: RunStage[];
  // Called with the clicked component's id.
  onOpen: (componentId: string) => void;
}) {
  const { currentOrg } = useOrg();
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    void (async () => {
      const { data, error } = await api.GET("/api/applications/{id}/workflow", {
        params: { path: { id: appId } },
      });
      if (cancelled) return;
      if (error || !data) {
        setError(error?.message ?? "Could not load the workflow");
      } else {
        setWorkflow(data);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, currentOrg?.id]);

  // The latest run's status per authored component id.
  const statusByComponent = useMemo(() => {
    const m: Record<string, ComponentRunStatus> = {};
    for (const st of latestStages ?? []) {
      for (const c of st.components) m[c.component_id] = c.status;
    }
    return m;
  }, [latestStages]);

  if (loading) {
    return <p className="px-4 py-6 text-sm text-neutral-400">Loading…</p>;
  }
  if (error) {
    return <p className="px-4 py-6 text-sm text-red-400">{error}</p>;
  }
  const stages = workflow?.stages ?? [];
  if (stages.every((st) => st.components.length === 0)) {
    return (
      <p className="px-4 py-6 text-sm text-neutral-400">
        No components yet. Open the workflow to add the first one.
      </p>
    );
  }
  return (
    <StageColumnsView
      stages={stages}
      clusterName={clusterName}
      statusByComponent={statusByComponent}
      onOpen={(c) => onOpen(c.id)}
    />
  );
}
