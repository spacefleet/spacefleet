import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { api } from "../api/client";

// Landing route for every GitHub redirect in the connect flow — the App's
// Setup URL and its Redirect URI both point here:
//
//   1. Connect sends the browser to GitHub's install page with a state token
//      (connect-url) binding the flow to this organization.
//   2. GitHub redirects to the Setup URL: ?installation_id=…&setup_action=…
//      &state=…. That installation_id is only a query parameter, so it can't
//      be trusted on its own; we continue to GitHub's OAuth authorize page
//      (authorize-url), which returns a code proving the user can access it.
//   3. GitHub redirects to the Redirect URI: ?code=…&state=…. We post both to
//      record the installation, then go to Admin › GitHub.
//
// An App that requests user authorization during installation skips step 2:
// GitHub sends the code with the installation_id straight away. With
// "Redirect on update", GitHub also comes back here (no state) after an
// installation's repositories or permissions change; there's nothing to
// record, so that just returns to Admin › GitHub.
//
// Control returns to the app *through React Router* (a raw
// history.replaceState would desync the router — see AuthCallback).
export function GitHubCallback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // Guard against the effect running twice (React 18 StrictMode) and posting
  // or redirecting twice.
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;

    const code = params.get("code");
    const state = params.get("state");
    const setupAction = params.get("setup_action");
    const rawId = params.get("installation_id");
    const installationId = rawId === null ? null : Number(rawId);
    if (
      installationId !== null &&
      !(Number.isInteger(installationId) && installationId > 0)
    ) {
      setError("Invalid installation id from GitHub.");
      return;
    }

    if (code && state) {
      void (async () => {
        const { error } = await api.POST("/api/github/installations", {
          body: {
            state,
            code,
            ...(installationId !== null && { installation_id: installationId }),
          },
        });
        if (error) {
          setError(error.message ?? "Could not record the GitHub installation.");
          return;
        }
        navigate("/admin/github", { replace: true });
      })();
      return;
    }

    if (setupAction === "request") {
      setNotice(
        "GitHub asked an owner of the account to approve the installation. Once it's approved, connect it from Admin › GitHub.",
      );
      return;
    }

    if (installationId !== null && state) {
      void (async () => {
        const { data, error } = await api.GET(
          "/api/github/installations/authorize-url",
          { params: { query: { installation_id: installationId, state } } },
        );
        if (error || !data) {
          setError(error?.message ?? "Could not continue the GitHub connection.");
          return;
        }
        window.location.href = data.url;
      })();
      return;
    }

    if (setupAction === "update") {
      navigate("/admin/github", { replace: true });
      return;
    }

    setError(
      "Missing installation details from GitHub. Start the connection from Admin › GitHub.",
    );
  }, [params, navigate]);

  return (
    <div className="flex h-screen items-center justify-center bg-gray-50">
      {error || notice ? (
        <div className="max-w-md text-center">
          <p className={`text-sm ${error ? "text-red-600" : "text-neutral-700"}`}>
            {error ?? notice}
          </p>
          <button
            type="button"
            onClick={() => navigate("/admin/github", { replace: true })}
            className="mt-4 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
          >
            Back to GitHub
          </button>
        </div>
      ) : (
        <p className="text-sm text-gray-500">Connecting GitHub…</p>
      )}
    </div>
  );
}
