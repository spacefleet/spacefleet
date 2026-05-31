import { Navigate, Outlet } from "react-router";
import { useOrg } from "../contexts/OrgContext";

// OrgGate enforces the rule that you must belong to (and have selected) an
// organization to use the app. While memberships are loading it shows a
// spinner; a user with no organizations is sent to the create-org screen.
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
    return <Navigate to="/organizations/new" replace />;
  }

  return <Outlet />;
}
