import { Navigate, Outlet } from "react-router";
import { useOrg } from "../contexts/OrgContext";
import { orgCreationEnabled } from "../lib/appConfig";
import { NoOrganizations } from "./NoOrganizations";

// OrgGate enforces the rule that you must belong to (and have selected) an
// organization to use the app. While memberships are loading it shows a
// spinner. A user with no organizations is sent to the create-org screen —
// unless this server has organization creation disabled, in which case there's
// nothing for them to create, so they're told to request an invite instead.
// Otherwise it renders the app via <Outlet />.
export function OrgGate() {
  const { loading, memberships } = useOrg();

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center bg-gray-50">
        <p className="text-sm text-gray-500">Loading…</p>
      </div>
    );
  }

  if (memberships.length === 0) {
    return orgCreationEnabled() ? (
      <Navigate to="/organizations/new" replace />
    ) : (
      <NoOrganizations />
    );
  }

  return <Outlet />;
}
