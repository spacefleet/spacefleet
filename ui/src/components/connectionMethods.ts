import type { components } from "../api/schema";

type ConnectionMethod = components["schemas"]["ConnectionMethod"];
type CreateRequest = components["schemas"]["ClusterCreateRequest"];

export type FieldType = "text" | "secret" | "textarea" | "checkbox";

export interface FieldDef {
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

export interface MethodDef {
  value: ConnectionMethod;
  label: string;
  description: string;
  fields: FieldDef[];
}

// CONNECTION_METHODS drives both the method picker and the per-method form. The
// field keys line up with ClusterCreateRequest, so the request body is built
// generically. It lives in its own module so the dialog and the list page can
// both import it (the list page uses it to label a cluster's method).
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
