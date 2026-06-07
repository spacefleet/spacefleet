import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type CloudCredential = components["schemas"]["CloudCredential"];
type CloudProvider = components["schemas"]["CloudProvider"];
type CreateRequest = components["schemas"]["CloudCredentialCreateRequest"];

const PROVIDER_LABELS: Record<CloudProvider, string> = {
  aws: "AWS",
  gcp: "Google Cloud",
  azure: "Azure",
};

// CloudCredentials is the Admin › Cloud Credentials page: it lists the cloud
// provider credentials (AWS, GCP, Azure) registered in the current organization
// and opens a dialog to add more. These are the foundation other features build
// on (cluster registration, private packages in workflows, …). Secret material
// is sealed server-side and never returned — the list shows only the name,
// provider, description, and non-secret config. An organization may register as
// many as it wants, including several of the same provider.
// Org-scoped: the X-Organization-ID header is attached automatically (see
// api/client.ts).
export function CloudCredentials() {
  const { currentOrg, currentRole } = useOrg();
  const canEdit = currentRole !== "viewer";
  const [creds, setCreds] = useState<CloudCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/cloud-credentials");
    if (error) setError(error.message ?? "Could not load cloud credentials");
    setCreds(data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function onDelete(c: CloudCredential) {
    if (!confirm(`Delete the credential "${c.name}"?`)) return;
    const { error } = await api.DELETE("/api/cloud-credentials/{id}", {
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
            Cloud Credentials
          </h1>
          <p className="mt-1 text-sm text-neutral-600">
            Cloud provider credentials (AWS, GCP, Azure) used to authenticate to
            a cloud — for cluster registration, private packages in workflows,
            and more. Secrets are encrypted at rest and never shown again.
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
              No cloud credentials yet
            </p>
            <p className="mt-1 text-sm text-neutral-500">
              Add one to let Spacefleet authenticate to your cloud account.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Provider</th>
                <th className="px-4 py-2 font-medium">Details</th>
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
                    {c.description && (
                      <span className="mt-0.5 block text-xs font-normal text-neutral-500">
                        {c.description}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    <span className="inline-flex border border-neutral-300 px-2 py-0.5 text-xs font-medium text-neutral-700">
                      {PROVIDER_LABELS[c.provider]}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {configSummary(c) || "—"}
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

// configSummary renders the non-secret identifiers we know about for display,
// so duplicates of the same provider stay distinguishable in the list.
function configSummary(c: CloudCredential): string {
  const cfg = c.config ?? {};
  const parts: string[] = [];
  if (c.provider === "aws") {
    if (cfg.region) parts.push(cfg.region);
    if (cfg.role_arn) parts.push(cfg.role_arn);
  } else if (c.provider === "gcp") {
    if (cfg.project) parts.push(cfg.project);
  } else if (c.provider === "azure") {
    if (cfg.subscription_id) parts.push(`sub ${cfg.subscription_id}`);
    if (cfg.tenant_id) parts.push(`tenant ${cfg.tenant_id}`);
  }
  return parts.join(" · ");
}

// AddCredentialDialog registers a cloud credential. A provider select swaps the
// field set; secret fields are write-only and sealed server-side.
function AddCredentialDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (c: CloudCredential) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [provider, setProvider] = useState<CloudProvider>("aws");
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function setField(key: string, value: string) {
    setFields((f) => ({ ...f, [key]: value }));
  }

  function onProviderChange(p: CloudProvider) {
    setProvider(p);
    setFields({});
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    const body: CreateRequest = { name: name.trim(), provider };
    if (description.trim() !== "") body.description = description.trim();
    for (const [key, value] of Object.entries(fields)) {
      const v = value.trim();
      if (v !== "") (body as Record<string, unknown>)[key] = v;
    }
    const { data, error } = await api.POST("/api/cloud-credentials", { body });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not create credential");
      return;
    }
    onCreated(data);
  }

  const ready =
    name.trim() !== "" &&
    PROVIDER_FIELDS[provider]
      .filter((f) => f.required)
      .every((f) => (fields[f.key] ?? "").trim() !== "");

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
      <div className="mt-12 w-full max-w-lg border border-neutral-200 bg-white shadow-lg">
        <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <KeyRound className="h-5 w-5 text-neutral-500" />
            New cloud credential
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-neutral-400 hover:text-neutral-700"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <form onSubmit={onSubmit} className="space-y-4 px-5 py-4">
          <Labeled htmlFor="cc-name" label="Name">
            <input
              id="cc-name"
              className="w-full border border-neutral-300 px-3 py-2 text-sm"
              placeholder="production-aws"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
          </Labeled>

          <Labeled htmlFor="cc-provider" label="Provider">
            <select
              id="cc-provider"
              className="w-full border border-neutral-300 bg-white px-3 py-2 text-sm"
              value={provider}
              onChange={(e) => onProviderChange(e.target.value as CloudProvider)}
            >
              <option value="aws">AWS</option>
              <option value="gcp">Google Cloud</option>
              <option value="azure">Azure</option>
            </select>
          </Labeled>

          <Labeled
            htmlFor="cc-description"
            label="Description"
            help="Optional — helps tell duplicates apart."
          >
            <input
              id="cc-description"
              className="w-full border border-neutral-300 px-3 py-2 text-sm"
              placeholder="Billing account, us-east-1"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Labeled>

          {PROVIDER_FIELDS[provider].map((f) => (
            <Labeled
              key={f.key}
              htmlFor={f.key}
              label={f.required ? f.label : `${f.label} (optional)`}
              help={f.help}
            >
              {f.kind === "textarea" ? (
                <textarea
                  id={f.key}
                  className="h-32 w-full border border-neutral-300 px-3 py-2 font-mono text-xs"
                  placeholder={f.placeholder}
                  value={fields[f.key] ?? ""}
                  onChange={(e) => setField(f.key, e.target.value)}
                  required={f.required}
                />
              ) : (
                <input
                  id={f.key}
                  type={f.kind === "secret" ? "password" : "text"}
                  className="w-full border border-neutral-300 px-3 py-2 text-sm"
                  placeholder={f.placeholder}
                  value={fields[f.key] ?? ""}
                  onChange={(e) => setField(f.key, e.target.value)}
                  autoComplete={f.kind === "secret" ? "new-password" : "off"}
                  required={f.required}
                />
              )}
            </Labeled>
          ))}

          {error && <p className="text-sm text-red-600">{error}</p>}

          <div className="flex items-center justify-end gap-3 border-t border-neutral-200 pt-4">
            <button
              type="button"
              onClick={onClose}
              className="text-sm text-neutral-500 hover:text-neutral-800"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!ready || submitting}
              className="bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
            >
              {submitting ? "Saving…" : "Add credential"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

type FieldKind = "text" | "secret" | "textarea";

interface ProviderField {
  key: string;
  label: string;
  kind: FieldKind;
  required: boolean;
  placeholder?: string;
  help?: string;
}

// PROVIDER_FIELDS declares the inputs per provider. Keys match the flat
// create-request fields the server validates and splits into config vs. sealed
// secret (see lib/api/cloud_credential_fields.go).
const PROVIDER_FIELDS: Record<CloudProvider, ProviderField[]> = {
  aws: [
    { key: "aws_access_key_id", label: "Access key ID", kind: "text", required: true },
    { key: "aws_secret_access_key", label: "Secret access key", kind: "secret", required: true },
    { key: "aws_session_token", label: "Session token", kind: "secret", required: false, help: "For temporary credentials." },
    { key: "aws_region", label: "Default region", kind: "text", required: false, placeholder: "us-east-1" },
    { key: "aws_role_arn", label: "Role ARN", kind: "text", required: false, help: "Role to assume after authenticating." },
  ],
  gcp: [
    {
      key: "gcp_service_account_key",
      label: "Service account key (JSON)",
      kind: "textarea",
      required: true,
      placeholder: '{ "type": "service_account", … }',
    },
    { key: "gcp_project", label: "Project ID", kind: "text", required: false, placeholder: "my-project" },
  ],
  azure: [
    { key: "azure_tenant_id", label: "Tenant ID", kind: "text", required: true },
    { key: "azure_client_id", label: "Client ID", kind: "text", required: true },
    { key: "azure_client_secret", label: "Client secret", kind: "secret", required: true },
    { key: "azure_subscription_id", label: "Subscription ID", kind: "text", required: false },
  ],
};

function Labeled({
  label,
  htmlFor,
  help,
  children,
}: {
  label: string;
  htmlFor: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label
        htmlFor={htmlFor}
        className="mb-1 block text-sm font-medium text-neutral-700"
      >
        {label}
      </label>
      {children}
      {help && <p className="mt-1 text-xs italic text-neutral-500">{help}</p>}
    </div>
  );
}
