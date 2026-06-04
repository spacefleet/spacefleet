import { useCallback, useEffect, useState } from "react";
import { GitBranch, Plus, Trash2 } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { githubAppEnabled } from "../lib/appConfig";
import type { components } from "../api/schema";

type GitHubInstallation = components["schemas"]["GitHubInstallation"];

// GitHubInstallations is the Admin › GitHub page: it lists the organization's
// installations of the operator's GitHub App, used to pull charts from private
// Git repositories, and starts the connect flow. No secret is stored — the
// access token is minted on demand at rollout time; the list shows only the
// account the App is installed on. An installation attached to an application
// can't be deleted (the API returns 409). Org-scoped: the X-Organization-ID
// header is attached automatically (see api/client.ts).
export function GitHubInstallations() {
  const { currentOrg, currentRole } = useOrg();
  const canEdit = currentRole !== "viewer";
  const appEnabled = githubAppEnabled();
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [connecting, setConnecting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/github/installations");
    if (error) setError(error.message ?? "Could not load GitHub installations");
    setInstallations(data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function onConnect() {
    setConnecting(true);
    setError(null);
    const { data, error } = await api.GET("/api/github/installations/connect-url");
    if (error || !data) {
      setConnecting(false);
      setError(error?.message ?? "Could not start the GitHub connection");
      return;
    }
    // Hand the browser to GitHub's install flow; it redirects back to the
    // /github/callback route, which records the installation.
    window.location.href = data.url;
  }

  async function onDelete(inst: GitHubInstallation) {
    const label = inst.account_login || String(inst.installation_id);
    if (!confirm(`Remove the GitHub installation "${label}"?`)) return;
    const { error } = await api.DELETE("/api/github/installations/{id}", {
      params: { path: { id: inst.id } },
    });
    if (error) {
      setError(error.message ?? "Could not remove installation");
      return;
    }
    setInstallations((xs) => xs.filter((x) => x.id !== inst.id));
  }

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Admin
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">GitHub</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Connect a GitHub App installation to deploy charts from private Git
            repositories. Attach one to a Git-source application.
          </p>
        </div>
        {canEdit && appEnabled && (
          <button
            type="button"
            onClick={() => void onConnect()}
            disabled={connecting}
            className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            <Plus className="h-4 w-4" />
            {connecting ? "Connecting…" : "Connect GitHub"}
          </button>
        )}
      </div>

      {!appEnabled && (
        <p className="mt-4 border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800">
          No GitHub App is configured on this deployment. Ask your operator to
          register one to enable pulling charts from private Git repositories.
        </p>
      )}

      <div className="mt-6 border border-neutral-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-neutral-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : installations.length === 0 ? (
          <div className="p-10 text-center">
            <GitBranch className="mx-auto h-8 w-8 text-neutral-300" />
            <p className="mt-3 text-sm font-medium text-neutral-700">
              No GitHub installations yet
            </p>
            <p className="mt-1 text-sm text-neutral-500">
              Connect one to pull charts from a private Git repository.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Account</th>
                <th className="px-4 py-2 font-medium">Type</th>
                <th className="px-4 py-2 font-medium">Installation ID</th>
                {canEdit && <th className="px-4 py-2" />}
              </tr>
            </thead>
            <tbody>
              {installations.map((inst) => (
                <tr
                  key={inst.id}
                  className="border-b border-neutral-100 last:border-0"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    {inst.account_login || "—"}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {inst.account_type || "—"}
                  </td>
                  <td className="px-4 py-3 font-mono text-neutral-600">
                    {inst.installation_id}
                  </td>
                  {canEdit && (
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => void onDelete(inst)}
                        className="inline-flex items-center gap-1 text-xs text-neutral-500 hover:text-red-600"
                        aria-label={`Remove ${inst.account_login || inst.installation_id}`}
                      >
                        <Trash2 className="h-4 w-4" />
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
