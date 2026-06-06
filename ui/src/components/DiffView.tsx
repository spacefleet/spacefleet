// DiffView renders the `helm diff` output captured by a refresh, coloring added
// (+), removed (-), and changed (~/header) lines so a deploy's effect on the
// live cluster reads at a glance. Plain monospace block; hue is reserved for the
// +/- lines (neutral elsewhere), per the brand.
export function DiffView({ diff }: { diff: string }) {
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <pre className="max-h-96 overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-300">
      {lines.map((line, i) => (
        <div key={i} className={lineClass(line)}>
          {line || " "}
        </div>
      ))}
    </pre>
  );
}

// lineClass colors a single diff line by its leading marker. helm-diff prints
// removals with a leading "-", additions with "+", and resource/object headers
// (e.g. "apps, web, Deployment (apps) has changed:") as plain lines.
function lineClass(line: string): string {
  const t = line.trimStart();
  if (t.startsWith("+")) return "text-green-400";
  if (t.startsWith("-")) return "text-red-400";
  if (t.includes("has changed") || t.includes("has been added") || t.includes("has been removed")) {
    return "text-neutral-100 font-medium";
  }
  return "";
}
