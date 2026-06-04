// formatDuration renders the elapsed time of a rollout run. While running (no
// finish time) it shows the time since it started with a "(running)" suffix;
// once finished, the start→finish span.
export function formatDuration(
  createdAt: string,
  finishedAt?: string | null,
): string {
  const start = new Date(createdAt).getTime();
  const end = finishedAt ? new Date(finishedAt).getTime() : Date.now();
  const secs = Math.max(0, Math.round((end - start) / 1000));
  const suffix = finishedAt ? "" : " (running)";
  if (secs < 60) return `${secs}s${suffix}`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return `${m}m ${s}s${suffix}`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m${suffix}`;
}
