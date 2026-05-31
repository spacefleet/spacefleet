import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useAuth } from "react-oidc-context";

// Landing route for the Dex redirect (redirect_uri = /auth/callback).
// react-oidc-context processes the ?code/&state on load; once it finishes we
// hand control back to the app *through React Router* so the router's location
// stays in sync. (A raw window.history.replaceState would change the URL bar
// without telling the router, leaving it rendering the wrong route — e.g. the
// NotFound page.)
export function AuthCallback() {
  const auth = useAuth();
  const navigate = useNavigate();

  useEffect(() => {
    if (!auth.isLoading && !auth.activeNavigator) {
      navigate("/", { replace: true });
    }
  }, [auth.isLoading, auth.activeNavigator, navigate]);

  return (
    <div className="flex h-screen items-center justify-center bg-gray-50">
      <p className="text-sm text-gray-500">Signing in…</p>
    </div>
  );
}
