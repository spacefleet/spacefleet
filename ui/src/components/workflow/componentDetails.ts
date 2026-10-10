import { CHART_SOURCES } from "../chartSources";
import { TOFU_DEFAULT_VERSION } from "../../lib/tofuVersions";
import { parseBackendConfig } from "./backendConfig";
import type { EditableComponent } from "./ComponentFields";

export interface DetailRow {
  label: string;
  value: string;
}

// The names a component's ids resolve to, and the application name a Helm
// release name defaults from.
export interface DetailLookups {
  appName?: string;
  clusterName: (id: string) => string | undefined;
  chartCredentialName: (id: string) => string | undefined;
  cloudCredentialName: (id: string) => string | undefined;
}

const BACKEND_LABELS: Record<string, string> = {
  spacefleet: "Spacefleet (managed)",
  s3: "Amazon S3",
  gcs: "Google Cloud Storage",
  azurerm: "Azure Blob Storage",
};

// componentDetails lists a component's settings for its page, read-only and
// in plain words: what it deploys and from where, where it deploys to (or, for
// OpenTofu, where its state lives and how it authenticates), then the
// approval gate and what a failure does. Unset optional settings read as their
// effective default ("Default branch", "Latest") rather than blank.
export function componentDetails(
  c: EditableComponent,
  lookups: DetailLookups,
): DetailRow[] {
  const cfg = c.config;
  const cluster = (id: string | null | undefined) =>
    id ? (lookups.clusterName(id) ?? id) : "";
  const rows: DetailRow[] = [];

  switch (c.type) {
    case "terraform": {
      const backend = cfg.backend || "s3";
      rows.push(
        { label: "Repository", value: cfg.repo_url ?? "" },
        { label: "Branch or tag", value: cfg.git_ref || "Default branch" },
        { label: "Working path", value: cfg.path || "Repository root" },
        {
          label: "OpenTofu version",
          value: cfg.tofu_version || TOFU_DEFAULT_VERSION,
        },
        { label: "State backend", value: BACKEND_LABELS[backend] ?? backend },
      );
      const location = stateLocation(backend, cfg.backend_config);
      if (location) rows.push({ label: "State location", value: location });
      if (backend !== "spacefleet" && cfg.workspace) {
        rows.push({ label: "Workspace", value: cfg.workspace });
      }
      rows.push({
        label: "Cloud credential",
        value: cfg.cloud_credential_id
          ? (lookups.cloudCredentialName(cfg.cloud_credential_id) ??
            cfg.cloud_credential_id)
          : "Runner's identity",
      });
      if (cfg.auth_cluster_id) {
        rows.push({
          label: "Cluster authentication",
          value: cluster(cfg.auth_cluster_id),
        });
      }
      break;
    }
    case "helm": {
      const source =
        CHART_SOURCES.find((s) => s.value === cfg.chart_source) ??
        CHART_SOURCES[0];
      rows.push({ label: "Chart source", value: source.label });
      for (const f of source.fields) {
        rows.push({
          label: f.label,
          value: cfg[f.key] || (f.key === "version" ? "Latest" : ""),
        });
      }
      rows.push(
        { label: "Release name", value: helmReleaseName(c, lookups.appName) },
        { label: "Target cluster", value: cluster(c.target_cluster_id) },
        { label: "Target namespace", value: c.target_namespace },
      );
      if (source.value !== "git") {
        rows.push({
          label: "Chart credential",
          value: c.chart_credential_id
            ? (lookups.chartCredentialName(c.chart_credential_id) ??
              c.chart_credential_id)
            : "None (public chart)",
        });
      }
      break;
    }
    case "manifest":
      rows.push(
        { label: "Repository", value: cfg.repo_url ?? "" },
        { label: "Branch or tag", value: cfg.git_ref || "Default branch" },
        { label: "Path", value: cfg.path ?? "" },
        { label: "Target cluster", value: cluster(c.target_cluster_id) },
      );
      break;
  }

  rows.push(
    { label: "Approval", value: approvalSummary(c) },
    {
      label: "On failure",
      value: c.continue_on_failure
        ? "Later stages still run"
        : "Later stages don't run",
    },
  );
  return rows;
}

// stateLocation names where a cloud backend keeps the state, in the
// provider's own address form; empty for managed state or an incomplete
// config.
export function stateLocation(backend: string, raw: string | undefined): string {
  const b = parseBackendConfig(raw);
  switch (backend) {
    case "s3":
      return b.bucket ? `s3://${b.bucket}/${b.key ?? ""}` : "";
    case "gcs":
      return b.bucket ? `gs://${b.bucket}/${b.prefix ?? ""}` : "";
    case "azurerm":
      return b.storage_account_name
        ? [b.storage_account_name, b.container_name, b.key]
            .filter(Boolean)
            .join(" / ")
        : "";
    default:
      return "";
  }
}

// helmReleaseName is the release a Helm component installs: its own setting,
// or "<application>-<component>" by default.
export function helmReleaseName(c: EditableComponent, appName?: string): string {
  return c.config.release_name || `${appName ?? "<application>"}-${c.name}`;
}

// approvalSummary describes the gate: off, or on with whatever its policy
// narrows (how many approvals, from whom, and a timeout).
function approvalSummary(c: EditableComponent): string {
  if (!c.requires_approval) return "Not required";
  const p = c.approval_policy;
  const parts = ["Required"];
  const n = p?.required ?? 0;
  if (n > 1) parts.push(`${n} approvals`);
  if (p?.approvers?.length) parts.push(`from ${p.approvers.join(", ")}`);
  if (p?.require_different_approver)
    parts.push("not by whoever started the run");
  if (p?.timeout_minutes)
    parts.push(`times out after ${p.timeout_minutes} min`);
  return parts.join(" · ");
}
