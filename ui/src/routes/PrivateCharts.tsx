import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { useDocumentTitle } from "../lib/useDocumentTitle";
import { Breadcrumbs } from "../components/Breadcrumbs";

type ChartCredential = components["schemas"]["ChartCredential"];
type CreateRequest = components["schemas"]["ChartCredentialCreateRequest"];

// PrivateCharts is the Admin › Private Charts page: it lists the credential sets
// used to pull private Helm charts in the current organization, and opens a
// dialog to add more. A credential is a basic-auth username/password that works
// for both HTTP Helm repositories and OCI registries. Passwords are sealed
// server-side and never returned — the list shows only name and username. A
// credential attached to an application can't be deleted (the API returns 409).
// Org-scoped: the X-Organization-ID header is attached automatically (see
// api/client.ts).
export function PrivateCharts() {
  const { currentOrg, currentRole } = useOrg();
  useDocumentTitle("Private Charts");
  const canEdit = currentRole !== "viewer";
  const [creds, setCreds] = useState<ChartCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/chart-credentials");
    if (error) setError(error.message ?? "Could not load chart credentials");
    setCreds(data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function onDelete(c: ChartCredential) {
    if (!confirm(`Delete the credential "${c.name}"?`)) return;
    const { error } = await api.DELETE("/api/chart-credentials/{id}", {
      params: { path: { id: c.id } },
    });
    if (error) {
      setError(error.message ?? "Could not delete credential");
      return;
    }
    setCreds((cs) => cs.filter((x) => x.id !== c.id));
  }

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <Breadcrumbs items={[{ label: "Admin" }]} />
          <h1 className="mt-2 text-2xl font-bold tracking-tight">
            Private Charts
          </h1>
          <p className="mt-1 text-sm text-neutral-300">
            Credentials for pulling private Helm charts. Attach one to an
            application when its chart lives in a private repo or registry.
          </p>
        </div>
        {canEdit && (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex shrink-0 items-center gap-2 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover"
          >
            <Plus className="h-4 w-4" />
            Add credential
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-800 bg-neutral-900">
        {loading ? (
          <p className="p-6 text-sm text-neutral-400">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-400">{error}</p>
        ) : creds.length === 0 ? (
          <div className="p-10 text-center">
            <KeyRound className="mx-auto h-8 w-8 text-neutral-600" />
            <p className="mt-3 text-sm font-medium text-neutral-300">
              No chart credentials yet
            </p>
            <p className="mt-1 text-sm text-neutral-400">
              Add one to pull charts from a private repository or registry.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Username</th>
                {canEdit && <th className="px-4 py-2" />}
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr
                  key={c.id}
                  className="border-b border-neutral-800 last:border-0"
                >
                  <td className="px-4 py-3 font-medium text-neutral-100">
                    {c.name}
                  </td>
                  <td className="px-4 py-3 text-neutral-300">
                    {c.username || "—"}
                  </td>
                  {canEdit && (
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => void onDelete(c)}
                        className="inline-flex items-center gap-1 text-xs text-neutral-400 hover:text-red-400"
                        aria-label={`Delete ${c.name}`}
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

      {adding && (
        <AddCredentialDialog
          onClose={() => setAdding(false)}
          onCreated={(c) => {
            setAdding(false);
            setCreds((cs) => [...cs, c]);
          }}
        />
      )}
    </div>
  );
}

// AddCredentialDialog is the modal that registers a chart credential: a name and
// the username/password. The password is sent once and sealed server-side; it's
// never read back.
function AddCredentialDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (c: ChartCredential) => void;
}) {
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    const body: CreateRequest = {
      name: name.trim(),
      password,
    };
    if (username.trim() !== "") body.username = username.trim();
    const { data, error } = await api.POST("/api/chart-credentials", { body });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not create credential");
      return;
    }
    onCreated(data);
  }

  const ready = name.trim() !== "" && password !== "";

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/70 p-4">
      <div className="mt-12 w-full max-w-lg border border-neutral-800 bg-neutral-900 shadow-lg">
        <div className="flex items-center justify-between border-b border-neutral-800 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <KeyRound className="h-5 w-5 text-neutral-400" />
            New chart credential
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-neutral-500 hover:text-neutral-300"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <form onSubmit={onSubmit} className="space-y-4 px-5 py-4">
          <Labeled label="Name">
            <input
              className="w-full border border-neutral-700 px-3 py-2 text-sm"
              placeholder="docker-hub"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
          </Labeled>

          <Labeled
            label="Username"
            help="Used for both HTTP Helm repositories and OCI registries."
          >
            <input
              className="w-full border border-neutral-700 px-3 py-2 text-sm"
              placeholder="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
            />
          </Labeled>

          <Labeled label="Password" help="Stored encrypted; never shown again.">
            <input
              type="password"
              className="w-full border border-neutral-700 px-3 py-2 text-sm"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Labeled>

          {error && <p className="text-sm text-red-400">{error}</p>}

          <div className="flex items-center justify-end gap-3 border-t border-neutral-800 pt-4">
            <button
              type="button"
              onClick={onClose}
              className="text-sm text-neutral-400 hover:text-neutral-200"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!ready || submitting}
              className="bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
            >
              {submitting ? "Saving…" : "Add credential"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Labeled({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="mb-1 block text-sm font-medium text-neutral-300">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-400">{help}</p>}
    </div>
  );
}
