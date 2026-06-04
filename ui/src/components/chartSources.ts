import type { components } from "../api/schema";

type ChartSource = components["schemas"]["ChartSource"];

export type ChartFieldType = "text" | "textarea";

export interface ChartFieldDef {
  // key is a key in the application's `config` map (snake_case), e.g. repo_url.
  key: string;
  label: string;
  type: ChartFieldType;
  required?: boolean;
  placeholder?: string;
  help?: string;
}

export interface ChartSourceDef {
  value: ChartSource;
  label: string;
  description: string;
  fields: ChartFieldDef[];
}

// CHART_SOURCES drives both the source picker and the per-source form. The field
// keys line up with the application's `config` map, so the request body's config
// is built generically. It lives in its own module so the create dialog and the
// detail page can both import it (the detail page uses it to label a source).
export const CHART_SOURCES: ChartSourceDef[] = [
  {
    value: "http_repo",
    label: "HTTP Helm repository",
    description:
      "A classic Helm repository served over HTTP (helm repo add). Public charts only.",
    fields: [
      {
        key: "repo_url",
        label: "Repository URL",
        type: "text",
        required: true,
        placeholder: "https://charts.bitnami.com/bitnami",
      },
      {
        key: "chart",
        label: "Chart name",
        type: "text",
        required: true,
        placeholder: "nginx",
      },
      {
        key: "version",
        label: "Version",
        type: "text",
        placeholder: "(latest if empty)",
        help: "Chart version to pin; leave empty for the latest published version.",
      },
    ],
  },
  {
    value: "oci",
    label: "OCI registry",
    description:
      "A chart stored in an OCI registry, referenced by its oci:// URL. Public charts only.",
    fields: [
      {
        key: "repo_url",
        label: "Chart reference",
        type: "text",
        required: true,
        placeholder: "oci://registry-1.docker.io/bitnamicharts/nginx",
      },
      {
        key: "version",
        label: "Version",
        type: "text",
        placeholder: "(latest if empty)",
        help: "Chart version to pin; leave empty for the latest published version.",
      },
    ],
  },
  {
    value: "git",
    label: "Git repository",
    description:
      "A chart checked out from a Git repository. Public, or private via a connected GitHub App (Admin › GitHub).",
    fields: [
      {
        key: "repo_url",
        label: "Repository URL",
        type: "text",
        required: true,
        placeholder: "https://github.com/org/charts.git",
      },
      {
        key: "git_ref",
        label: "Branch or tag",
        type: "text",
        placeholder: "(default branch if empty)",
      },
      {
        key: "git_path",
        label: "Chart path",
        type: "text",
        placeholder: "charts/app",
        help: "Path to the chart directory within the repository (repo root if empty).",
      },
    ],
  },
];

export function chartSourceLabel(source: ChartSource): string {
  return CHART_SOURCES.find((s) => s.value === source)?.label ?? source;
}
