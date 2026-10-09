import { useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import type { components } from "../../api/schema";

type ComponentRunOutput = components["schemas"]["ComponentRunOutput"];

// OutputsTable lists the OpenTofu outputs captured from a settled apply step.
// A sensitive output is masked by default; when the API sent its value (it
// omits sensitive values for callers below editor), an eye toggle reveals and
// re-masks it — a viewer sees only the masked entry, with nothing to reveal.
export function OutputsTable({
  entries,
}: {
  entries: [string, ComponentRunOutput][];
}) {
  const [revealed, setRevealed] = useState<Set<string>>(new Set());
  const toggle = (name: string) =>
    setRevealed((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  return (
    <div className="h-full overflow-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
            <th className="px-2 py-1.5 font-medium">Output</th>
            <th className="w-full px-2 py-1.5 font-medium">Value</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([name, output]) => {
            const masked = output.sensitive && !revealed.has(name);
            // The API omits a sensitive value below editor — then there is
            // nothing to reveal.
            const revealable =
              output.sensitive &&
              output.value !== undefined &&
              output.value !== null;
            return (
              <tr key={name} className="border-b border-neutral-800 align-top">
                <td className="whitespace-nowrap px-2 py-1.5 font-mono text-xs text-neutral-100">
                  {name}
                </td>
                <td className="px-2 py-1.5">
                  <span className="inline-flex items-center gap-2">
                    {masked ? (
                      <span className="font-mono text-xs text-neutral-500">
                        ••••••••
                      </span>
                    ) : (
                      <span className="break-all font-mono text-xs text-neutral-100">
                        {formatOutputValue(output.value)}
                      </span>
                    )}
                    {revealable && (
                      <button
                        type="button"
                        onClick={() => toggle(name)}
                        aria-label={
                          masked ? `Reveal ${name}` : `Mask ${name}`
                        }
                        title={
                          masked
                            ? "Reveal this sensitive value"
                            : "Mask this value again"
                        }
                        className="p-0.5 text-neutral-500 hover:text-neutral-100"
                      >
                        {masked ? (
                          <Eye className="h-3.5 w-3.5" />
                        ) : (
                          <EyeOff className="h-3.5 w-3.5" />
                        )}
                      </button>
                    )}
                  </span>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// formatOutputValue renders one output value: strings bare, everything else
// (numbers, booleans, lists, objects) as compact JSON.
function formatOutputValue(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value);
}
