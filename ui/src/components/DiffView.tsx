// DiffView renders the `helm diff` output captured by a refresh, coloring added
// (+), removed (-), and changed (~/header) lines so a deploy's effect on the
// live cluster reads at a glance. Plain monospace block; hue is reserved for the
// +/- lines (neutral elsewhere), per the brand. className sizes the block —
// the default caps it for inline embedding; pass e.g. "h-full" to fill a panel.
export function DiffView({
  diff,
  className = "max-h-96",
}: {
  diff: string;
  className?: string;
}) {
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <pre
      className={`overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-300 ${className}`}
    >
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
