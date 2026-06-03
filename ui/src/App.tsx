import type { ReactNode } from "react";
import { Route, Routes } from "react-router";
import { ApiAuthBinder } from "./components/ApiAuthBinder";
import { AuthGate } from "./components/AuthGate";
import { Layout } from "./components/Layout";
import { OrgGate } from "./components/OrgGate";
import { OrgProvider } from "./contexts/OrgContext";
import { AcceptInvite } from "./routes/AcceptInvite";
import { AuthCallback } from "./routes/AuthCallback";
import { Clusters } from "./routes/Clusters";
import { ClusterDetail } from "./routes/ClusterDetail";
import { CreateOrganization } from "./routes/CreateOrganization";
import { Home } from "./routes/Home";
import { Login } from "./routes/Login";
import { Members } from "./routes/Members";
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
  "/providers/clusters": <Clusters />,
  "/infrastructure/nodes": <Nodes />,
  "/infrastructure/namespaces": <Namespaces />,
  "/infrastructure/pods": <Pods />,
  "/organization/members": <Members />,
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
                {/* Cluster drill-down: the per-cluster management page, reached
                    by clicking a row on the Clusters leaf (not a nav entry of
                    its own, so it's added here rather than in nav.ts). */}
                <Route
                  path="/providers/clusters/:clusterId"
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
