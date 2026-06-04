import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type ChartCredential = components["schemas"]["ChartCredential"];
type ChartCredentialType = components["schemas"]["ChartCredentialType"];
type CreateRequest = components["schemas"]["ChartCredentialCreateRequest"];

// Credential types and how they read in the UI. The type matches the chart
// source it authenticates (basic_auth → HTTP repo, oci → OCI registry).
const CREDENTIAL_TYPES: { value: ChartCredentialType; label: string; help: string }[] = [
  {
    value: "basic_auth",
    label: "Basic auth (HTTP repo)",
    help: "Username/password for an HTTP Helm repository (helm repo add).",
  },
  {
    value: "oci",
    label: "OCI registry",
    help: "Username/password for an OCI registry (helm registry login).",
  },
];

function typeLabel(t: ChartCredentialType): string {
  return CREDENTIAL_TYPES.find((c) => c.value === t)?.label ?? t;
}

// PrivateCharts is the Admin › Private Charts page: it lists the credential sets
// used to pull private Helm charts in the current organization, and opens a
// dialog to add more. Passwords are sealed server-side and never returned — the
// list shows only name, type, and username. A credential attached to an
// application can't be deleted (the API returns 409). Org-scoped: the
// X-Organization-ID header is attached automatically (see api/client.ts).
export function PrivateCharts() {
  const { currentOrg, currentRole } = useOrg();
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
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Admin
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">
            Private Charts
          </h1>
          <p className="mt-1 text-sm text-neutral-600">
            Credentials for pulling private Helm charts. Attach one to an
            application when its chart lives in a private repo or registry.
          </p>
        </div>
        {canEdit && (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
          >
            <Plus className="h-4 w-4" />
            Add credential
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-neutral-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : creds.length === 0 ? (
          <div className="p-10 text-center">
            <KeyRound className="mx-auto h-8 w-8 text-neutral-300" />
            <p className="mt-3 text-sm font-medium text-neutral-700">
              No chart credentials yet
            </p>
            <p className="mt-1 text-sm text-neutral-500">
              Add one to pull charts from a private repository or registry.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Type</th>
                <th className="px-4 py-2 font-medium">Username</th>
                {canEdit && <th className="px-4 py-2" />}
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr
                  key={c.id}
                  className="border-b border-neutral-100 last:border-0"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    {c.name}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {typeLabel(c.type)}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {c.username || "—"}
                  </td>
                  {canEdit && (
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => void onDelete(c)}
                        className="inline-flex items-center gap-1 text-xs text-neutral-500 hover:text-red-600"
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

// AddCredentialDialog is the modal that registers a chart credential: a name, a
// type, and the username/password. The password is sent once and sealed
// server-side; it's never read back.
function AddCredentialDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (c: ChartCredential) => void;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState<ChartCredentialType>("basic_auth");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const selected = CREDENTIAL_TYPES.find((t) => t.value === type)!;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    const body: CreateRequest = {
      name: name.trim(),
      type,
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
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-lg border border-gray-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-gray-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <KeyRound className="h-5 w-5 text-gray-500" />
            New chart credential
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-gray-400 hover:text-gray-700"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <form onSubmit={onSubmit} className="space-y-4 px-5 py-4">
          <Labeled label="Name">
            <input
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="docker-hub"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
          </Labeled>

          <Labeled label="Type">
            <select
              className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
              value={type}
              onChange={(e) => setType(e.target.value as ChartCredentialType)}
            >
              {CREDENTIAL_TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-gray-500">{selected.help}</p>
          </Labeled>

          <Labeled label="Username">
            <input
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
            />
          </Labeled>

          <Labeled label="Password" help="Stored encrypted; never shown again.">
            <input
              type="password"
              className="w-full border border-gray-300 px-3 py-2 text-sm"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Labeled>

          {error && <p className="text-sm text-red-600">{error}</p>}

          <div className="flex items-center justify-end gap-3 border-t border-gray-200 pt-4">
            <button
              type="button"
              onClick={onClose}
              className="text-sm text-gray-500 hover:text-gray-800"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!ready || submitting}
              className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
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
      <label className="mb-1 block text-sm font-medium text-gray-700">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-gray-500">{help}</p>}
    </div>
  );
}
