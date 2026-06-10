import { useCallback, useEffect, useState } from "react";
import { Copy, Check, Mail, Trash2, UserPlus } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { emailEnabled } from "../lib/appConfig";
import type { components } from "../api/schema";

type Member = components["schemas"]["Member"];
type Invitation = components["schemas"]["Invitation"];
type Role = components["schemas"]["Role"];

const ROLES: { value: Role; label: string; hint: string }[] = [
  { value: "admin", label: "Admin", hint: "Manage the organization, members, and everything an editor can do" },
  { value: "editor", label: "Editor", hint: "Take any action in the app, but not manage the organization" },
  { value: "viewer", label: "Viewer", hint: "Read-only access to everything" },
];

// Members is the Organization › Members page (admin-only): it lists the
// organization's members with their roles, lets an admin change a role or
// remove a member, and manages invitations — including copy-able invite links
// for when email isn't configured.
export function Members() {
  const { currentOrg, currentRole } = useOrg();

  const [members, setMembers] = useState<Member[]>([]);
  const [invites, setInvites] = useState<Invitation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [m, i] = await Promise.all([
      api.GET("/api/members"),
      api.GET("/api/invitations"),
    ]);
    if (m.error) setError(m.error.message ?? "Could not load members");
    setMembers(m.data ?? []);
    setInvites(i.data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    if (currentRole !== "admin") return;
    void load();
  }, [load, currentOrg?.id, currentRole]);

  if (currentRole !== "admin") {
    return (
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Members</h1>
        <p className="mt-2 text-sm text-neutral-600">
          You need to be an organization admin to manage members.
        </p>
      </div>
    );
  }

  async function onChangeRole(userId: string, role: Role) {
    const { data, error } = await api.PATCH("/api/members/{userId}", {
      params: { path: { userId } },
      body: { role },
    });
    if (error) {
      setError(error.message ?? "Could not change role");
      return;
    }
    if (data) setMembers((ms) => ms.map((m) => (m.user_id === userId ? data : m)));
  }

  async function onRemove(userId: string) {
    if (!confirm("Remove this member from the organization?")) return;
    const { error } = await api.DELETE("/api/members/{userId}", {
      params: { path: { userId } },
    });
    if (error) {
      setError(error.message ?? "Could not remove member");
      return;
    }
    setMembers((ms) => ms.filter((m) => m.user_id !== userId));
  }

  async function onRevoke(id: string) {
    const { error } = await api.DELETE("/api/invitations/{id}", {
      params: { path: { id } },
    });
    if (error) {
      setError(error.message ?? "Could not revoke invitation");
      return;
    }
    setInvites((is) => is.filter((i) => i.id !== id));
  }

  return (
    <div className="space-y-8">
      <div>
        <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
          Organization
        </p>
        <h1 className="mt-1 text-2xl font-bold tracking-tight">Members</h1>
        <p className="mt-1 text-sm text-neutral-600">
          Manage who can access {currentOrg?.name ?? "this organization"} and what they can do.
        </p>
      </div>

      {error && (
        <p className="border border-red-200 bg-red-50 p-3 text-sm text-red-700">
          {error}
        </p>
      )}

      <InviteForm onInvited={(inv) => setInvites((is) => [inv, ...is])} />

      <section>
        <h2 className="text-sm font-semibold text-neutral-700">Members</h2>
        <div className="mt-2 border border-neutral-200 bg-white">
          {loading ? (
            <p className="p-6 text-sm text-neutral-500">Loading…</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                  <th className="px-4 py-2 font-medium">Email</th>
                  <th className="px-4 py-2 font-medium">Role</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {members.map((m) => (
                  <tr key={m.user_id} className="border-b border-neutral-100 last:border-0">
                    <td className="px-4 py-3 font-medium text-neutral-900">{m.email}</td>
                    <td className="px-4 py-3">
                      <select
                        value={m.role}
                        onChange={(e) => void onChangeRole(m.user_id, e.target.value as Role)}
                        className="border border-neutral-300 bg-white px-2 py-1 text-sm"
                      >
                        {ROLES.map((r) => (
                          <option key={r.value} value={r.value}>
                            {r.label}
                          </option>
                        ))}
                      </select>
                    </td>
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => void onRemove(m.user_id)}
                        className="inline-flex items-center gap-1.5 text-red-500 hover:text-red-700"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </section>

      {invites.length > 0 && (
        <section>
          <h2 className="text-sm font-semibold text-neutral-700">Pending invitations</h2>
          <p className="mt-1 text-xs text-neutral-500">
            Invite links are shown only when created. To get a new link for an
            address, invite it again — the old link stops working.
          </p>
          <div className="mt-2 border border-neutral-200 bg-white">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                  <th className="px-4 py-2 font-medium">Email</th>
                  <th className="px-4 py-2 font-medium">Role</th>
                  <th className="px-4 py-2 font-medium">Expires</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {invites.map((inv) => (
                  <tr key={inv.id} className="border-b border-neutral-100 last:border-0">
                    <td className="px-4 py-3 font-medium text-neutral-900">{inv.email}</td>
                    <td className="px-4 py-3 capitalize text-neutral-600">{inv.role}</td>
                    <td className="px-4 py-3 text-neutral-600">
                      {new Date(inv.expires_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-3 text-right whitespace-nowrap">
                      <button
                        type="button"
                        onClick={() => void onRevoke(inv.id)}
                        className="inline-flex items-center gap-1.5 text-red-500 hover:text-red-700"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}

// InviteForm invites a new user. On success it surfaces the copy-able link
// (always) and whether an email was sent (when SMTP is configured).
function InviteForm({ onInvited }: { onInvited: (inv: Invitation) => void }) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ url: string; emailSent: boolean } | null>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    setResult(null);
    const { data, error } = await api.POST("/api/invitations", {
      body: { email: email.trim(), role },
    });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not create invitation");
      return;
    }
    onInvited(data.invitation);
    setResult({ url: data.accept_url, emailSent: data.email_sent });
    setEmail("");
  }

  return (
    <section>
      <h2 className="text-sm font-semibold text-neutral-700">Invite a user</h2>
      <form
        onSubmit={onSubmit}
        className="mt-2 flex flex-wrap items-end gap-3 border border-neutral-200 bg-white p-4"
      >
        <div className="flex-1 min-w-[16rem]">
          <label className="block text-xs font-medium text-neutral-500">Email</label>
          <input
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="person@example.com"
            className="mt-1 w-full border border-neutral-300 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-neutral-500">Role</label>
          <select
            value={role}
            onChange={(e) => setRole(e.target.value as Role)}
            className="mt-1 border border-neutral-300 bg-white px-2 py-2 text-sm"
          >
            {ROLES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </div>
        <button
          type="submit"
          disabled={!email.trim() || submitting}
          className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
        >
          <UserPlus className="h-4 w-4" />
          {submitting ? "Inviting…" : "Send invite"}
        </button>
      </form>

      {error && <p className="mt-2 text-sm text-red-600">{error}</p>}

      {result && (
        <div className="mt-2 border border-neutral-200 bg-neutral-50 p-4 text-sm">
          <p className="flex items-center gap-2 font-medium text-neutral-800">
            <Mail className="h-4 w-4" />
            {result.emailSent
              ? "Invitation email sent."
              : emailEnabled()
                ? "Invitation created (email could not be sent — share the link below)."
                : "Invitation created. Email isn't configured on this server — copy the link below and send it yourself."}
          </p>
          <div className="mt-2 flex items-center gap-2">
            <input
              readOnly
              value={result.url}
              onFocus={(e) => e.currentTarget.select()}
              className="flex-1 border border-neutral-300 bg-white px-3 py-2 text-xs text-neutral-700"
            />
            <CopyLinkButton url={result.url} />
          </div>
        </div>
      )}
    </section>
  );
}

// CopyLinkButton copies an invite link to the clipboard, flashing a check.
function CopyLinkButton({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(url);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
      className="inline-flex items-center gap-1.5 text-neutral-500 hover:text-neutral-900"
    >
      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      {copied ? "Copied" : "Copy link"}
    </button>
  );
}
