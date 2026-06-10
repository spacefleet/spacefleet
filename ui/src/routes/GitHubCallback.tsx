import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { api } from "../api/client";

// Landing route for GitHub's post-install redirect (the App's Callback URL
// points at /github/callback, with "Request user authorization (OAuth) during
// installation" enabled). GitHub appends
// ?code=…&installation_id=…&setup_action=…&state=… The state token, issued by
// connect-url, binds the install back to the initiating organization, and the
// code lets the backend verify the installing user actually has access to the
// installation; we POST all three to record it, then hand control back to the
// app *through React Router* (a raw history.replaceState would desync the
// router — see AuthCallback).
export function GitHubCallback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  // Guard against the effect running twice (React 18 StrictMode) and POSTing
  // the installation twice; the second POST is idempotent server-side, but this
  // keeps the UX clean.
  const submitted = useRef(false);

  useEffect(() => {
    if (submitted.current) return;
    submitted.current = true;

    const installationIdRaw = params.get("installation_id");
    const state = params.get("state");
    const code = params.get("code");
    if (!installationIdRaw || !state || !code) {
      setError("Missing installation details from GitHub.");
      return;
    }
    const installationId = Number(installationIdRaw);
    if (!Number.isInteger(installationId)) {
      setError("Invalid installation id from GitHub.");
      return;
    }

    void (async () => {
      const { error } = await api.POST("/api/github/installations", {
        body: { installation_id: installationId, state, code },
      });
      if (error) {
        setError(error.message ?? "Could not record the GitHub installation.");
        return;
      }
      navigate("/admin/github", { replace: true });
    })();
  }, [params, navigate]);

  return (
    <div className="flex h-screen items-center justify-center bg-gray-50">
      {error ? (
        <div className="max-w-md text-center">
          <p className="text-sm text-red-600">{error}</p>
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
