import type { ReactNode } from "react";
import { Route, Routes } from "react-router";
import { ApiAuthBinder } from "./components/ApiAuthBinder";
import { AuthGate } from "./components/AuthGate";
import { Layout } from "./components/Layout";
import { OrgGate } from "./components/OrgGate";
import { OrgProvider } from "./contexts/OrgContext";
import { AcceptInvite } from "./routes/AcceptInvite";
import { AuthCallback } from "./routes/AuthCallback";
import { Applications } from "./routes/Applications";
import { ApplicationForm } from "./routes/ApplicationForm";
import { ImportApplication } from "./routes/ImportApplication";
import { ApplicationDetail } from "./routes/ApplicationDetail";
import { WorkflowBuilder } from "./routes/WorkflowBuilder";
import { WorkflowRuns } from "./routes/WorkflowRuns";
import { WorkflowRunView } from "./routes/WorkflowRunView";
import { Clusters } from "./routes/Clusters";
import { ClusterDetail } from "./routes/ClusterDetail";
import { CreateOrganization } from "./routes/CreateOrganization";
import { GitHubCallback } from "./routes/GitHubCallback";
import { GitHubInstallations } from "./routes/GitHubInstallations";
import { Home } from "./routes/Home";
import { Login } from "./routes/Login";
import { Members } from "./routes/Members";
import { PrivateCharts } from "./routes/PrivateCharts";
import { NamespaceDetail } from "./routes/NamespaceDetail";
import { Namespaces } from "./routes/Namespaces";
import { NodeDetail } from "./routes/NodeDetail";
import { Nodes } from "./routes/Nodes";
import { PodDetail } from "./routes/PodDetail";
import { Pods } from "./routes/Pods";
import { NotFound } from "./routes/NotFound";
import { Placeholder } from "./routes/Placeholder";
import { navLeaves } from "./nav";

// Real page components, keyed by nav path. Any leaf not listed here renders the
// scaffolded Placeholder — swap entries in as pages are built.
const pageComponents: Record<string, ReactNode> = {
  "/applications": <Applications />,
  "/admin/clusters": <Clusters />,
  "/infrastructure/nodes": <Nodes />,
  "/infrastructure/namespaces": <Namespaces />,
  "/infrastructure/pods": <Pods />,
  "/admin/members": <Members />,
  "/admin/private-charts": <PrivateCharts />,
  "/admin/github": <GitHubInstallations />,
};

export function App() {
  return (
    <Routes>
      {/* Dex redirect target. Kept outside the AuthGate group so it can run
          the code exchange and route home itself; its specific path outranks
          the nested catch-all below. */}
      <Route path="/auth/callback" element={<AuthCallback />} />

      {/* Explicit sign-in screen. Kept outside the AuthGate group so it's
          reachable while unauthenticated — AuthGate redirects here (it no longer
          auto-launches the Dex flow), and sign-out lands here too. */}
      <Route path="/login" element={<Login />} />

      {/* ApiAuthBinder installs the bearer-token provider for the API client.
          AuthGate then requires a Dex session — redirecting to /login if absent.
          OrgProvider loads the user's organizations; OrgGate then requires one
          to be selected before the app renders, sending org-less users to the
          create-org screen (which lives above OrgGate so it stays reachable). */}
      <Route element={<ApiAuthBinder />}>
        <Route element={<AuthGate />}>
          <Route element={<OrgProvider />}>
            <Route path="/organizations/new" element={<CreateOrganization />} />
            {/* Invite acceptance lives above the OrgGate so a brand-new user
                with no organization yet can still reach it after signing in. */}
            <Route path="/invite/:token" element={<AcceptInvite />} />
            {/* GitHub App post-install redirect. Above the OrgGate (but inside
                OrgProvider, so the selected org's header is sent with the POST)
                so the connect flow completes regardless of the gate, then it
                routes to Admin › GitHub itself. */}
            <Route path="/github/callback" element={<GitHubCallback />} />
            <Route element={<OrgGate />}>
              <Route element={<Layout />}>
                {/* Dashboard overview (the "/" leaf) is served by Home; every
                    other nav leaf gets a scaffolded Placeholder page. Routes are
                    generated from the nav config in nav.ts — swap a Placeholder
                    for a real component as each page is built. */}
                <Route index element={<Home />} />
                {navLeaves
                  .filter(({ leaf }) => leaf.path !== "/")
                  .map(({ leaf }) => (
                    <Route
                      key={leaf.path}
                      path={leaf.path}
                      element={pageComponents[leaf.path] ?? <Placeholder />}
                    />
                  ))}
                {/* Application create/edit: the full-page form, reached from the
                    Applications leaf's "Create app" button and the detail page's
                    "Edit" button. "/new" is listed before the ":appId" detail
                    route below so it isn't captured as an app id. */}
                <Route path="/applications/new" element={<ApplicationForm />} />
                {/* Import: the discovery step (pick a cluster, list its Helm
                    releases) hands off to ApplicationForm in import mode via
                    router state. Listed before ":appId" so it isn't an app id. */}
                <Route
                  path="/applications/import"
                  element={<ImportApplication />}
                />
                <Route
                  path="/applications/:appId/edit"
                  element={<ApplicationForm />}
                />
                {/* Workflow canvas: the interactive DAG builder for an app's
                    deploy workflow. Listed before the ":appId" detail route so
                    the "/workflow" segment isn't swallowed (it can't be — the
                    detail route has no trailing segment — but kept adjacent for
                    clarity). */}
                <Route
                  path="/applications/:appId/workflow"
                  element={<WorkflowBuilder />}
                />
                {/* Workflow run history (a CI-like list of runs). */}
                <Route
                  path="/applications/:appId/runs"
                  element={<WorkflowRuns />}
                />
                {/* One workflow run's live DAG view (status-colored nodes +
                    per-component logs/diff). */}
                <Route
                  path="/applications/:appId/runs/:runId"
                  element={<WorkflowRunView />}
                />
                {/* Application drill-down: the per-app management page, reached
                    by clicking a row on the Applications leaf (not a nav entry
                    of its own, so it's added here rather than in nav.ts). */}
                <Route
                  path="/applications/:appId"
                  element={<ApplicationDetail />}
                />
                {/* Cluster drill-down: the per-cluster management page, reached
                    by clicking a row on the Clusters leaf (not a nav entry of
                    its own, so it's added here rather than in nav.ts). */}
                <Route
                  path="/admin/clusters/:clusterId"
                  element={<ClusterDetail />}
                />
                {/* Node drill-down: a detail route under the Nodes leaf, not a
                    nav entry of its own (so it's added here, not in nav.ts). */}
                <Route
                  path="/infrastructure/nodes/:clusterId/:nodeName"
                  element={<NodeDetail />}
                />
                {/* Namespace drill-down: a detail route under the Namespaces
                    leaf, not a nav entry of its own (so it's added here, not in
                    nav.ts). */}
                <Route
                  path="/infrastructure/namespaces/:clusterId/:namespaceName"
                  element={<NamespaceDetail />}
                />
                {/* Pod drill-down: a detail route under the Pods leaf (not a
                    nav entry of its own). The namespace segment disambiguates
                    pods of the same name across namespaces. */}
                <Route
                  path="/infrastructure/pods/:clusterId/:namespace/:podName"
                  element={<PodDetail />}
                />
                <Route path="*" element={<NotFound />} />
              </Route>
            </Route>
          </Route>
        </Route>
      </Route>
    </Routes>
  );
}
