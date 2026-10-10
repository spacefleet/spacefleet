import { useParams } from "react-router";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import { Breadcrumbs } from "../components/Breadcrumbs";
import { VariablesEditor } from "../components/VariablesEditor";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// ComponentVariables is one component's variables page, reached from its
// page's Variables button: variables only this component's job gets,
// overriding app-level ones of the same name. They're stored by component id,
// so a component the workflow hasn't saved yet has none to show. Viewers see
// the list read-only.
export function ComponentVariables() {
  const { appId = "", nodeId = "" } = useParams();
  const { appName, canEdit, loading, getComponent, isProvisional } =
    useWorkflowDraft();
  const component = getComponent(nodeId);
  useDocumentTitle("Variables", component?.name, appName);
  const workflowPath = `/applications/${appId}/workflow`;

  return (
    <div>
      <Breadcrumbs
        items={[
          { label: "Applications", to: "/applications" },
          { label: appName ?? "…", to: `/applications/${appId}` },
          { label: "Workflow", to: workflowPath },
          ...(component && !isProvisional(nodeId)
            ? [{ label: component.name, to: `${workflowPath}/nodes/${nodeId}` }]
            : []),
        ]}
      />
      <h1 className="mt-2 text-xl font-bold tracking-tight">Variables</h1>
      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : !component || isProvisional(nodeId) ? (
        <p className="mt-6 text-sm text-neutral-300">
          That component isn’t saved in this workflow.
        </p>
      ) : (
        <>
          <p className="mt-1 text-sm text-neutral-300">
            Passed to <span className="font-medium">{component.name}</span>
            ’s job as environment variables, overriding any app-level variable
            of the same name.
            {component.type === "terraform" &&
              " Each also sets the module’s input variable of the same name, if it declares one."}{" "}
            A sensitive value is sealed and never shown again.
          </p>
          <div className="mt-6 border border-neutral-800 bg-neutral-900 p-4">
            <VariablesEditor
              scope={{ kind: "component", appId, componentId: nodeId }}
              canEdit={canEdit}
            />
          </div>
        </>
      )}
    </div>
  );
}
