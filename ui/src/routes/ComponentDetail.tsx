import { useState } from "react";
import { Navigate, useLocation, useNavigate, useParams } from "react-router";
import { ArrowLeft, ArrowRightLeft, Settings, Trash2, Variable } from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ComponentStatePanel } from "../components/workflow/ComponentStatePanel";
import { DeleteComponentDialog } from "../components/workflow/DeleteComponentDialog";
import { MoveComponentDialog } from "../components/workflow/MoveComponentDialog";
import { componentDetails } from "../components/workflow/componentDetails";
import { COMPONENT_TYPE_LABELS } from "../components/workflow/stageView";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// ComponentDetail is one workflow component's page
// (/applications/:appId/workflow/nodes/:nodeId), shaped like the application
// page: the component's settings at a glance and — for OpenTofu — its recorded
// state, with Variables, Manage (the editor), and Move and Delete in the
// header's menu. A
// component still being created has no page yet, so it opens in the editor.
//
// The run view of a component's succeeded destroy/uninstall links back here
// with { deleteAfterUninstall: true } in the location state, which opens the
// delete dialog with its question already answered.
export function ComponentDetail() {
  const { appId = "", nodeId = "" } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const afterUninstall =
    (location.state as { deleteAfterUninstall?: boolean } | null)
      ?.deleteAfterUninstall === true;
  const {
    appName,
    canEdit,
    loading,
    error,
    getComponent,
    isProvisional,
    stageOf,
    clusters,
    credentials,
    cloudCredentials,
    removeComponent,
    flush,
    reload,
  } = useWorkflowDraft();
  const component = getComponent(nodeId);
  const placed = stageOf(nodeId);
  useDocumentTitle(component?.name, "Workflow", appName);
  const [deleting, setDeleting] = useState(afterUninstall);
  const [moving, setMoving] = useState(false);

  const workflowPath = `/applications/${appId}/workflow`;
  const componentPath = `${workflowPath}/nodes/${nodeId}`;

  if (isProvisional(nodeId)) {
    return <Navigate to={`${componentPath}/edit`} replace />;
  }

  return (
    <div className="mx-auto flex max-w-4xl flex-col">
      <button
        type="button"
        onClick={() => navigate(workflowPath)}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-400 hover:text-neutral-100"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to workflow
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-400">{error}</p>
      ) : !component ? (
        <p className="mt-6 text-sm text-neutral-300">
          That component isn’t in this workflow.
        </p>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
                {COMPONENT_TYPE_LABELS[component.type]} component
                {placed && (
                  <span className="ml-1 normal-case tracking-normal">
                    · in stage {placed.index + 1}, {placed.stage.name}
                  </span>
                )}
              </p>
              <h1 className="mt-0.5 break-all text-xl font-bold tracking-tight">
                {component.name}
              </h1>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <button
                type="button"
                onClick={() => navigate(`${componentPath}/variables`)}
                className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
              >
                <Variable className="h-3.5 w-3.5" />
                Variables
              </button>
              {canEdit && (
                <>
                  <button
                    type="button"
                    onClick={() => navigate(`${componentPath}/edit`)}
                    className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
                  >
                    <Settings className="h-3.5 w-3.5" />
                    Manage
                  </button>
                  <ActionsMenu
                    label={`${component.name} actions`}
                    items={[
                      {
                        label: "Move…",
                        icon: <ArrowRightLeft className="h-3.5 w-3.5" />,
                        onSelect: () => setMoving(true),
                      },
                      {
                        label: "Delete",
                        icon: <Trash2 className="h-3.5 w-3.5" />,
                        danger: true,
                        onSelect: () => setDeleting(true),
                      },
                    ]}
                  />
                </>
              )}
            </div>
          </div>

          <div className="mt-6 border border-neutral-800 bg-neutral-900 p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
              Details
            </h2>
            <dl className="mt-3 grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2">
              {componentDetails(component, {
                appName,
                clusterName: (id) => clusters.find((c) => c.id === id)?.name,
                chartCredentialName: (id) =>
                  credentials.find((c) => c.id === id)?.name,
                cloudCredentialName: (id) =>
                  cloudCredentials.find((c) => c.id === id)?.name,
              }).map((row) => (
                <div key={row.label} className="flex min-w-0 flex-col">
                  <dt className="text-xs text-neutral-500">{row.label}</dt>
                  <dd className="break-all text-neutral-200">
                    {row.value || "—"}
                  </dd>
                </div>
              ))}
            </dl>
          </div>

          {/* An OpenTofu component's recorded state: the resources it manages
              and its outputs, as of its last successful apply, plus — for an
              editor — the guarded state operations and scoped runs. */}
          {component.type === "terraform" && (
            <ComponentStatePanel
              appId={appId}
              componentId={nodeId}
              canEdit={canEdit}
              managedBackend={component.config.backend === "spacefleet"}
            />
          )}

          {moving && canEdit && (
            <MoveComponentDialog
              appId={appId}
              component={component}
              onClose={() => setMoving(false)}
              onBeforeMove={flush}
              onMoved={(target) => {
                setMoving(false);
                // Within this application the draft is stale now; another
                // application's page loads its own.
                if (target === appId) void reload();
                navigate(`/applications/${target}/workflow/nodes/${nodeId}`);
              }}
            />
          )}

          {deleting && canEdit && (
            <DeleteComponentDialog
              appId={appId}
              appName={appName}
              component={component}
              clusterName={(id) => clusters.find((c) => c.id === id)?.name}
              afterUninstall={afterUninstall}
              onClose={() => {
                setDeleting(false);
                // Forget the run view's hand-off, so a reload or a later
                // Delete asks the question again.
                if (afterUninstall) navigate(componentPath, { replace: true });
              }}
              onDelete={(opts) => removeComponent(nodeId, opts)}
              onDeleted={() => navigate(workflowPath)}
              onUninstallStarted={(runId) =>
                navigate(`/applications/${appId}/runs/${runId}`)
              }
            />
          )}
        </>
      )}
    </div>
  );
}
