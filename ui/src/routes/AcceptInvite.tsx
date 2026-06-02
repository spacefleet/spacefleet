import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useAuth } from "react-oidc-context";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type InvitationPreview = components["schemas"]["InvitationPreview"];

// AcceptInvite is the destination of an invite link (/invite/:token). It sits
// above the OrgGate so users with no organization yet (brand-new accounts that
// just signed in through Dex) can still reach it. It previews what the invite
// is for, then — on accept — joins the organization, selects it, and routes
// home through the router.
export function AcceptInvite() {
  const { token = "" } = useParams();
  const navigate = useNavigate();
  const auth = useAuth();
  const { refresh, setCurrentOrg } = useOrg();

  const [preview, setPreview] = useState<InvitationPreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [accepting, setAccepting] = useState(false);

  useEffect(() => {
    let active = true;
    setLoading(true);
    void api
      .GET("/api/invites/{token}", { params: { path: { token } } })
      .then(({ data, error }) => {
        if (!active) return;
        if (error || !data) setError(error?.message ?? "This invitation could not be found.");
        else setPreview(data);
      })
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [token]);

  async function onAccept() {
    setAccepting(true);
    setError(null);
    const { data, error } = await api.POST("/api/invites/{token}/accept", {
      params: { path: { token } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not accept this invitation.");
      setAccepting(false);
      return;
    }
    // Refresh memberships so the new org appears, then select it and go home.
    await refresh();
    setCurrentOrg(data.id);
    navigate("/", { replace: true });
  }

  const usable = preview?.status === "pending" && !preview.expired;

  return (
    <div className="flex min-h-screen flex-col bg-neutral-50">
      <header className="flex h-14 shrink-0 items-center bg-black px-4 text-sm text-white">
        <span className="font-semibold tracking-tight">Spacefleet</span>
        <div className="ml-auto flex items-center gap-3">
          {auth.user?.profile.email && (
            <span className="text-neutral-300">{auth.user.profile.email}</span>
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
        <div className="w-full max-w-md border border-neutral-200 bg-white p-6">
          {loading ? (
            <p className="text-sm text-neutral-500">Loading invitation…</p>
          ) : error && !preview ? (
            <>
              <h1 className="text-xl font-bold tracking-tight">Invitation unavailable</h1>
              <p className="mt-2 text-sm text-neutral-600">{error}</p>
            </>
          ) : preview ? (
            <>
              <h1 className="text-xl font-bold tracking-tight">
                Join {preview.organization_name}
              </h1>
              <p className="mt-2 text-sm text-neutral-600">
                You've been invited to join{" "}
                <strong>{preview.organization_name}</strong> as a{" "}
                <strong>{preview.role}</strong>.
              </p>

              {!usable && (
                <p className="mt-4 border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
                  {preview.status === "accepted"
                    ? "This invitation has already been accepted."
                    : preview.status === "revoked"
                      ? "This invitation has been revoked."
                      : "This invitation has expired."}
                </p>
              )}

              {error && <p className="mt-3 text-sm text-red-600">{error}</p>}

              <div className="mt-5 flex items-center gap-3">
                <button
                  type="button"
                  disabled={!usable || accepting}
                  onClick={() => void onAccept()}
                  className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                >
                  {accepting ? "Joining…" : "Accept invitation"}
                </button>
                <button
                  type="button"
                  onClick={() => navigate("/", { replace: true })}
                  className="text-sm text-neutral-500 hover:text-neutral-800"
                >
                  Not now
                </button>
              </div>
            </>
          ) : null}
        </div>
      </main>
    </div>
  );
}
