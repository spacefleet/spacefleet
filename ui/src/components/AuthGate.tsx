import { useEffect } from "react";
import { Outlet } from "react-router";
import { useAuth, hasAuthParams } from "react-oidc-context";

// AuthGate enforces a logged-in session for everything it wraps. Unauthenticated
// users are redirected straight to Dex (no intermediate sign-in screen). Once
// authenticated it renders the app via <Outlet />.
//
// The redirect is fired from an effect (not during render) to avoid kicking it
// off while react-oidc-context is still loading from storage or processing the
// callback (?code/&state) on the /auth/callback return.
export function AuthGate() {
  const auth = useAuth();

  useEffect(() => {
    if (
      !hasAuthParams() &&
      !auth.isAuthenticated &&
      !auth.activeNavigator &&
      !auth.isLoading &&
      !auth.error
    ) {
      void auth.signinRedirect();
    }
    // Depend on the individual auth.* fields, not the whole `auth` object,
    // which react-oidc-context returns fresh each render (would re-fire the
    // effect every time).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    auth.isAuthenticated,
    auth.activeNavigator,
    auth.isLoading,
    auth.error,
    auth.signinRedirect,
  ]);

  if (auth.error) {
    return (
      <Centered>
        <div className="max-w-md space-y-4 text-center">
          <p className="text-sm text-gray-700">
            Sign-in failed: {auth.error.message}
          </p>
          <button
            type="button"
            onClick={() => void auth.signinRedirect()}
            className="bg-black px-4 py-2 text-sm font-semibold text-white hover:bg-gray-800"
          >
            Try again
          </button>
        </div>
      </Centered>
    );
  }

  if (auth.isAuthenticated) {
    return <Outlet />;
  }

  // Loading from storage, processing the callback, or about to redirect.
  return (
    <Centered>
      <p className="text-sm text-gray-500">Signing in…</p>
    </Centered>
  );
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-screen items-center justify-center bg-gray-50">
      {children}
    </div>
  );
}
