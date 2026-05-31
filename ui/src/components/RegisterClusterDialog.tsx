import { useState } from "react";
import { Server, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { CONNECTION_METHODS } from "./connectionMethods";

type Cluster = components["schemas"]["Cluster"];
type ConnectionMethod = components["schemas"]["ConnectionMethod"];
type CreateRequest = components["schemas"]["ClusterCreateRequest"];

type FormState = Record<string, string | boolean>;

// RegisterClusterDialog is a modal that registers a cluster. It picks a
// connection method, renders that method's fields, and POSTs to /api/clusters
// (which also probes the cluster and returns its connection status).
export function RegisterClusterDialog({
  onClose,
  onRegistered,
}: {
  onClose: () => void;
  onRegistered: (cluster: Cluster) => void;
}) {
  const [method, setMethod] = useState<ConnectionMethod>("in_cluster");
  const [name, setName] = useState("");
  const [values, setValues] = useState<FormState>({});
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const selected = CONNECTION_METHODS.find((m) => m.value === method)!;

  function setField(key: string, value: string | boolean) {
    setValues((v) => ({ ...v, [key]: value }));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);

    const body = { name: name.trim(), connection_method: method } as CreateRequest;
    for (const field of selected.fields) {
      const raw = values[field.key as string];
      if (field.type === "checkbox") {
        if (raw) (body as Record<string, unknown>)[field.key as string] = true;
      } else if (typeof raw === "string" && raw.trim() !== "") {
        (body as Record<string, unknown>)[field.key as string] = raw;
      }
    }

    const { data, error } = await api.POST("/api/clusters", { body });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not register cluster");
      return;
    }
    onRegistered(data);
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-lg border border-gray-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-gray-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <Server className="h-5 w-5 text-gray-500" />
            Add cluster
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
              placeholder="production"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
          </Labeled>

          <Labeled label="Connection method">
            <select
              className="w-full border border-gray-300 bg-white px-3 py-2 text-sm"
              value={method}
              onChange={(e) => {
                setMethod(e.target.value as ConnectionMethod);
                setValues({});
                setError(null);
              }}
            >
              {CONNECTION_METHODS.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-gray-500">{selected.description}</p>
          </Labeled>

          {selected.fields.map((field) =>
            field.type === "checkbox" ? (
              <label
                key={field.key as string}
                className="flex items-center gap-2 text-sm text-gray-700"
              >
                <input
                  type="checkbox"
                  checked={Boolean(values[field.key as string])}
                  onChange={(e) => setField(field.key as string, e.target.checked)}
                />
                {field.label}
              </label>
            ) : (
              <Labeled
                key={field.key as string}
                label={field.label}
                help={field.help}
                command={field.command}
              >
                {field.type === "textarea" ? (
                  <textarea
                    className="h-28 w-full border border-gray-300 px-3 py-2 font-mono text-xs"
                    placeholder={field.placeholder}
                    value={String(values[field.key as string] ?? "")}
                    onChange={(e) => setField(field.key as string, e.target.value)}
                    required={field.required}
                  />
                ) : (
                  <input
                    type={field.type === "secret" ? "password" : "text"}
                    className="w-full border border-gray-300 px-3 py-2 text-sm"
                    placeholder={field.placeholder}
                    value={String(values[field.key as string] ?? "")}
                    onChange={(e) => setField(field.key as string, e.target.value)}
                    required={field.required}
                  />
                )}
              </Labeled>
            ),
          )}

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
              disabled={!name.trim() || submitting}
              className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
            >
              {submitting ? "Registering…" : "Register & test"}
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
  command,
  children,
}: {
  label: string;
  help?: string;
  command?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="mb-1 block text-sm font-medium text-gray-700">
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-gray-500">{help}</p>}
      {command && (
        <code className="mt-1 block overflow-x-auto whitespace-pre bg-gray-100 px-2 py-1 font-mono text-[11px] text-gray-700">
          {command}
        </code>
      )}
    </div>
  );
}
