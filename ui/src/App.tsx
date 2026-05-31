import type { ReactNode } from "react";
import { Route, Routes } from "react-router";
import { ApiAuthBinder } from "./components/ApiAuthBinder";
import { AuthGate } from "./components/AuthGate";
import { Layout } from "./components/Layout";
import { OrgGate } from "./components/OrgGate";
import { OrgProvider } from "./contexts/OrgContext";
import { AuthCallback } from "./routes/AuthCallback";
import { Clusters } from "./routes/Clusters";
import { CreateOrganization } from "./routes/CreateOrganization";
import { Home } from "./routes/Home";
import { NotFound } from "./routes/NotFound";
import { Placeholder } from "./routes/Placeholder";
import { navLeaves } from "./nav";

// Real page components, keyed by nav path. Any leaf not listed here renders the
// scaffolded Placeholder — swap entries in as pages are built.
const pageComponents: Record<string, ReactNode> = {
  "/providers/clusters": <Clusters />,
};

export function App() {
  return (
    <Routes>
      {/* Dex redirect target. Kept outside the AuthGate group so it can run
          the code exchange and route home itself; its specific path outranks
          the nested catch-all below. */}
      <Route path="/auth/callback" element={<AuthCallback />} />

      {/* ApiAuthBinder installs the bearer-token provider for the API client.
          AuthGate then requires a Dex session — redirecting to login if absent.
          OrgProvider loads the user's organizations; OrgGate then requires one
          to be selected before the app renders, sending org-less users to the
          create-org screen (which lives above OrgGate so it stays reachable). */}
      <Route element={<ApiAuthBinder />}>
        <Route element={<AuthGate />}>
          <Route element={<OrgProvider />}>
            <Route path="/organizations/new" element={<CreateOrganization />} />
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
                <Route path="*" element={<NotFound />} />
              </Route>
            </Route>
          </Route>
        </Route>
      </Route>
    </Routes>
  );
}
