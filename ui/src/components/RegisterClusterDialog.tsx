import { useState } from "react";
import { Server, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";

type Cluster = components["schemas"]["Cluster"];
type ConnectionMethod = components["schemas"]["ConnectionMethod"];
type CreateRequest = components["schemas"]["ClusterCreateRequest"];

type FieldType = "text" | "secret" | "textarea" | "checkbox";

interface FieldDef {
  // key matches the ClusterCreateRequest property name (snake_case).
  key: keyof CreateRequest;
  label: string;
  type: FieldType;
  required?: boolean;
  placeholder?: string;
  // help is descriptive prose; command is a shell sample shown with code
  // styling beneath it (e.g. how to obtain the value).
  help?: string;
  command?: string;
}

interface MethodDef {
  value: ConnectionMethod;
  label: string;
  description: string;
  fields: FieldDef[];
}

// CONNECTION_METHODS drives both the method picker and the per-method form. The
// field keys line up with ClusterCreateRequest, so the request body is built
// generically. It's exported so the list page can label a cluster's method.
export const CONNECTION_METHODS: MethodDef[] = [
  {
    value: "in_cluster",
    label: "In-cluster",
    description:
      "Spacefleet is running in this cluster. Uses its own service account — no credentials needed.",
    fields: [],
  },
  {
    value: "kubeconfig",
    label: "Kubeconfig",
    description:
      "Paste a kubeconfig with embedded static credentials. Note: kubeconfigs that authenticate via an exec plugin (aws/gcloud/kubelogin) won't work server-side — use the matching cloud option instead.",
    fields: [
      {
        key: "kubeconfig",
        label: "Kubeconfig YAML",
        type: "textarea",
        required: true,
        placeholder: "apiVersion: v1\nkind: Config\n…",
        help: "Get a self-contained config (credentials inlined) for your current context:",
        command: "kubectl config view --minify --flatten",
      },
    ],
  },
  {
    value: "token",
    label: "Generic (token)",
    description:
      "Connect any cluster with its API server URL, CA certificate, and a ServiceAccount bearer token.",
    fields: [
      {
        key: "endpoint",
        label: "API server URL",
        type: "text",
        required: true,
        placeholder: "https://10.0.0.1:6443",
        help: "Find it in your kubeconfig:",
        command:
          "kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'",
      },
      {
        key: "ca",
        label: "CA certificate (PEM)",
        type: "textarea",
        placeholder: "-----BEGIN CERTIFICATE-----",
        help: "Required unless you skip TLS verification below. Extract it from your kubeconfig:",
        command:
          "kubectl config view --minify --flatten -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d",
      },
      {
        key: "insecure_skip_tls",
        label: "Skip TLS verification (insecure)",
        type: "checkbox",
      },
      {
        key: "token",
        label: "ServiceAccount bearer token",
        type: "secret",
        required: true,
        help: "A ServiceAccount token with RBAC for the resources Spacefleet manages. Mint one:",
        command: "kubectl create token <sa> -n <namespace> --duration=8760h",
      },
    ],
  },
  {
    value: "eks",
    label: "Amazon EKS",
    description:
      "Authenticate via AWS IAM. The API endpoint and CA are discovered from the cluster.",
    fields: [
      { key: "aws_region", label: "Region", type: "text", required: true, placeholder: "us-east-1" },
      {
        key: "eks_cluster_name",
        label: "Cluster name",
        type: "text",
        required: true,
        help: "The cluster name, not the ARN. List them:",
        command: "aws eks list-clusters --region <region>",
      },
      {
        key: "aws_access_key_id",
        label: "Access key ID",
        type: "text",
        required: true,
        help: "IAM credentials with eks:DescribeCluster and cluster access (an EKS access entry, or an aws-auth mapping).",
      },
      { key: "aws_secret_access_key", label: "Secret access key", type: "secret", required: true },
      { key: "aws_session_token", label: "Session token (optional)", type: "secret", help: "Only for temporary (STS) credentials." },
      { key: "aws_role_arn", label: "Role ARN to assume (optional)", type: "text", help: "Assume this role for cluster access after authenticating." },
    ],
  },
  {
    value: "gke",
    label: "Google GKE",
    description:
      "Authenticate with a GCP service-account key. The endpoint and CA are discovered from the cluster.",
    fields: [
      { key: "gcp_project", label: "Project ID", type: "text", required: true },
      {
        key: "gcp_location",
        label: "Location (region or zone)",
        type: "text",
        required: true,
        placeholder: "us-central1",
        help: "Region (us-central1) for regional clusters, or zone (us-central1-a) for zonal — match the cluster.",
      },
      {
        key: "gke_cluster_name",
        label: "Cluster name",
        type: "text",
        required: true,
        help: "List clusters:",
        command: "gcloud container clusters list",
      },
      {
        key: "gcp_service_account_key",
        label: "Service account JSON key",
        type: "textarea",
        required: true,
        placeholder: '{ "type": "service_account", … }',
        help: "Key for a service account with the Kubernetes Engine Cluster Viewer role. Create one:",
        command:
          "gcloud iam service-accounts keys create key.json --iam-account=<sa-email>",
      },
    ],
  },
  {
    value: "aks",
    label: "Azure AKS",
    description:
      "Authenticate with an Azure service principal. The endpoint and CA are discovered from the cluster.",
    fields: [
      { key: "azure_subscription_id", label: "Subscription ID", type: "text", required: true },
      { key: "azure_resource_group", label: "Resource group", type: "text", required: true },
      {
        key: "aks_cluster_name",
        label: "Cluster name",
        type: "text",
        required: true,
        help: "Look up the cluster's group and name:",
        command: "az aks list -o table",
      },
      {
        key: "azure_tenant_id",
        label: "Tenant ID",
        type: "text",
        required: true,
        help: "The service principal's directory (tenant) ID.",
      },
      { key: "azure_client_id", label: "Client ID", type: "text", required: true },
      {
        key: "azure_client_secret",
        label: "Client secret",
        type: "secret",
        required: true,
        help: "Service principal with the 'Azure Kubernetes Service Cluster User Role' on the cluster. Create one:",
        command: "az ad sp create-for-rbac",
      },
    ],
  },
];

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
