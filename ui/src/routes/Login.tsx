import { Navigate } from "react-router";
import { useAuth } from "react-oidc-context";
import icon from "@/assets/spacefleet-icon.svg";
import { loginMethods } from "../lib/appConfig";

// Login is the explicit sign-in screen. Unauthenticated users land here (sent by
// AuthGate) instead of being bounced straight into Dex, and sign-out returns
// here too — so logging out has a place to land and *stay*, rather than silently
// re-authenticating against a still-live identity-provider session.
//
// It renders one button per configured login method (window.appConfig
// .loginMethods, mirroring the operator's Dex connectors), each deep-linking to
// its connector via ?connector_id=<id> so the user skips Dex's own picker. When
// no methods are configured it falls back to a single generic "Sign in" button.
export function Login() {
  const auth = useAuth();
  const methods = loginMethods();

  // Already signed in (e.g. navigated to /login by hand): go home.
  if (auth.isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  // Mid-flow: react-oidc-context is loading from storage or a redirect is in
  // progress. Show a quiet placeholder rather than flashing the form.
  if (auth.isLoading || auth.activeNavigator) {
    return (
      <Centered>
        <p className="text-sm text-neutral-500">Signing in…</p>
      </Centered>
    );
  }

  return (
    <Centered>
      <div className="w-full max-w-sm space-y-8">
        <div className="space-y-3 text-center">
          <img src={icon} alt="" className="mx-auto h-10 w-10" />
          <h1 className="text-xl font-semibold tracking-tight text-neutral-900">
            Sign in to Spacefleet
          </h1>
        </div>

        {auth.error && (
          <p className="text-center text-sm text-red-600">
            Sign-in failed: {auth.error.message}
          </p>
        )}

        <div className="space-y-2">
          {methods.length === 0 ? (
            <SignInButton onClick={() => void auth.signinRedirect()}>
              Sign in
            </SignInButton>
          ) : (
            methods.map((method) => (
              <SignInButton
                key={method.id}
                onClick={() =>
                  void auth.signinRedirect({
                    extraQueryParams: { connector_id: method.id },
                  })
                }
              >
                Continue with {method.name}
              </SignInButton>
            ))
          )}
        </div>
      </div>
    </Centered>
  );
}

function SignInButton({
  onClick,
  children,
}: {
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="w-full bg-black px-4 py-2 text-sm font-semibold text-white hover:bg-neutral-800"
    >
      {children}
    </button>
  );
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-screen items-center justify-center bg-neutral-50 px-4">
      {children}
    </div>
  );
}
