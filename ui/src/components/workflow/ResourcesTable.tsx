import { useMemo, useState } from "react";
import type { components } from "../../api/schema";

type TofuResource = components["schemas"]["TofuResource"];

// ResourcesTable lists the resources an OpenTofu component manages, as
// recorded by its last successful apply: address, type, provider, and the
// provider-assigned id. A filter box narrows a long inventory by any of those;
// data sources are shown after managed resources, since they own nothing.
export function ResourcesTable({ resources }: { resources: TofuResource[] }) {
  const [filter, setFilter] = useState("");
  const rows = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const sorted = [...resources].sort((a, b) =>
      a.mode === b.mode ? 0 : a.mode === "data" ? 1 : -1,
    );
    if (!q) return sorted;
    return sorted.filter((r) =>
      [r.address, r.type, r.provider ?? "", formatId(r.id)].some((s) =>
        s.toLowerCase().includes(q),
      ),
    );
  }, [resources, filter]);

  const managed = resources.filter((r) => r.mode !== "data").length;
  const data = resources.length - managed;

  if (resources.length === 0) {
    return (
      <p className="text-sm text-neutral-500">
        No resources are recorded in this component's state.
      </p>
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-xs text-neutral-500">
          {managed} managed resource{managed === 1 ? "" : "s"}
          {data > 0 && (
            <>
              {" "}
              · {data} data source{data === 1 ? "" : "s"}
            </>
          )}
        </p>
        {resources.length > 8 && (
          <input
            type="search"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter by address, type, provider, or id"
            aria-label="Filter resources"
            className="ml-auto w-72 border border-neutral-300 px-2 py-1 text-xs focus:border-black focus:outline-none"
          />
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
              <th className="px-2 py-1.5 font-medium">Address</th>
              <th className="px-2 py-1.5 font-medium">Type</th>
              <th className="px-2 py-1.5 font-medium">Provider</th>
              <th className="w-full px-2 py-1.5 font-medium">ID</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={r.address}
                className={`border-b border-neutral-100 align-top ${
                  r.mode === "data" ? "text-neutral-500" : "text-neutral-900"
                }`}
              >
                <td className="whitespace-nowrap px-2 py-1.5 font-mono text-xs">
                  {r.address}
                </td>
                <td className="whitespace-nowrap px-2 py-1.5 font-mono text-xs">
                  {r.type}
                </td>
                <td className="whitespace-nowrap px-2 py-1.5 text-xs text-neutral-500">
                  {shortProvider(r.provider)}
                </td>
                <td className="break-all px-2 py-1.5 font-mono text-xs">
                  {formatId(r.id) || <span className="text-neutral-400">—</span>}
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={4} className="px-2 py-3 text-xs text-neutral-500">
                  No resources match "{filter}".
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// shortProvider trims a fully-qualified provider source
// (registry.opentofu.org/hashicorp/aws) to its last segment (aws), which is
// what people call it; anything without a slash is shown as-is.
function shortProvider(provider?: string): string {
  if (!provider) return "";
  const i = provider.lastIndexOf("/");
  return i >= 0 ? provider.slice(i + 1) : provider;
}

function formatId(id: unknown): string {
  if (id === undefined || id === null) return "";
  if (typeof id === "string") return id;
  return JSON.stringify(id);
}
