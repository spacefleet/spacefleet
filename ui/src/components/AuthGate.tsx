import { Navigate, Outlet } from "react-router";
import { useAuth } from "react-oidc-context";

// AuthGate enforces a logged-in session for everything it wraps. Unauthenticated
// users are sent to the explicit /login screen (it no longer auto-launches the
// Dex flow), so signing out lands on a real page instead of silently bouncing
// back through the identity provider. Once authenticated it renders the app via
// <Outlet />.
export function AuthGate() {
  const auth = useAuth();

  if (auth.isAuthenticated) {
    return <Outlet />;
  }

  // Still loading from storage or finishing a redirect: show a quiet
  // placeholder rather than flashing the login screen.
  if (auth.isLoading || auth.activeNavigator) {
    return (
      <div className="flex h-screen items-center justify-center bg-neutral-50">
        <p className="text-sm text-neutral-500">Signing in…</p>
      </div>
    );
  }

  // No session (or a sign-in error) — hand off to the login screen, which
  // surfaces any error and offers the sign-in buttons.
  return <Navigate to="/login" replace />;
}
