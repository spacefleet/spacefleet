import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useAuth } from "react-oidc-context";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";

// CreateOrganization is the destination for users with no organization (the
// OrgGate sends them here) and is also reachable from the navbar to spin up
// another one. On success it refreshes memberships, selects the new org, and
// routes home through React Router (never history.replaceState — that desyncs
// the router; see CLAUDE.md).
export function CreateOrganization() {
  const { memberships, refresh, setCurrentOrg } = useOrg();
  const navigate = useNavigate();
  const auth = useAuth();

  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // First-run users (no orgs yet) can't cancel — there's nowhere to go back to.
  const hasOrgs = memberships.length > 0;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    const { data, error } = await api.POST("/api/organizations", {
      body: { name: name.trim() },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not create organization");
      setSubmitting(false);
      return;
    }
    await refresh();
    setCurrentOrg(data.id);
    navigate("/", { replace: true });
  }

  return (
    <div className="flex min-h-screen flex-col bg-gray-50">
      <header className="flex h-14 shrink-0 items-center bg-black px-4 text-sm text-white">
        <span className="font-semibold tracking-tight">Spacefleet</span>
        <div className="ml-auto flex items-center gap-3">
          {auth.user?.profile.email && (
            <span className="text-gray-300">{auth.user.profile.email}</span>
          )}
          <button
            type="button"
            onClick={() => void auth.removeUser()}
            className="border border-white/30 px-3 py-1 font-medium text-white hover:bg-white/10"
          >
            Sign out
          </button>
        </div>
      </header>

      <main className="flex flex-1 items-center justify-center p-8">
        <div className="w-full max-w-md">
          <h1 className="text-2xl font-bold tracking-tight">
            {hasOrgs ? "New organization" : "Create your organization"}
          </h1>
          <p className="mt-1 text-sm text-gray-600">
            {hasOrgs
              ? "Spin up another organization to switch into."
              : "You need an organization to get started. Everything in Spacefleet lives inside one."}
          </p>

          <form
            onSubmit={onSubmit}
            className="mt-6 space-y-3 border border-gray-200 bg-white p-4"
          >
            <input
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="Organization name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
            {error && <p className="text-sm text-red-600">{error}</p>}
            <div className="flex items-center gap-3">
              <button
                type="submit"
                className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
                disabled={!name.trim() || submitting}
              >
                {submitting ? "Creating…" : "Create organization"}
              </button>
              {hasOrgs && (
                <Link to="/" className="text-sm text-gray-500 hover:text-gray-800">
                  Cancel
                </Link>
              )}
            </div>
          </form>
        </div>
      </main>
    </div>
  );
}
