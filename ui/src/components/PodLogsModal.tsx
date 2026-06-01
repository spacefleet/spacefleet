import { useEffect, useMemo, useRef, useState } from "react";
import { X } from "lucide-react";
import { usePodLogs } from "../lib/usePodLogs";
import type { StreamStatus } from "../lib/resourceStream";

interface PodLogsModalProps {
  clusterId: string;
  clusterName?: string;
  namespace: string;
  podName: string;
  // Container names from the pod's status; the first is selected by default.
  containers: string[];
  onClose: () => void;
}

// PodLogsModal streams one pod's logs into an overlay. It follows live (tailing
// the recent backlog first), lets the user pick a container on multi-container
// pods and toggle timestamps, and sticks to the bottom unless the user scrolls
// up to read history.
export function PodLogsModal({
  clusterId,
  clusterName,
  namespace,
  podName,
  containers,
  onClose,
}: PodLogsModalProps) {
  const [container, setContainer] = useState(containers[0] ?? "");
  // Off by default: long lines scroll horizontally rather than wrapping.
  const [wrap, setWrap] = useState(false);

  // Rebuilding this string is what (re)opens the stream — switching container
  // tears down the old follow and starts a fresh one. Timestamps are always
  // requested (the server prefixes each line); the viewer renders them muted.
  const path = useMemo(() => {
    const params = new URLSearchParams({ timestamps: "true" });
    if (container) params.set("container", container);
    return `/api/clusters/${clusterId}/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}/logs/stream?${params.toString()}`;
  }, [clusterId, namespace, podName, container]);

  const { lines, status, ended, error } = usePodLogs(path);

  // Close on Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Stick to the bottom as new lines arrive, unless the user has scrolled up.
  const scrollRef = useRef<HTMLPreElement>(null);
  const stickRef = useRef(true);
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="flex h-[80vh] w-full max-w-4xl flex-col border border-gray-200 bg-white shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-start justify-between gap-4 border-b border-gray-200 px-4 py-3">
          <div className="min-w-0">
            <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
              Logs{clusterName ? ` · ${clusterName}` : ""} · {namespace}
            </p>
            <h2 className="truncate text-lg font-bold tracking-tight">{podName}</h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close logs"
            className="shrink-0 p-1 text-gray-400 hover:text-gray-900"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        {/* Controls */}
        <div className="flex flex-wrap items-center gap-4 border-b border-gray-200 px-4 py-2">
          <LogStatus status={status} ended={ended} />
          {containers.length > 1 && (
            <label className="flex items-center gap-2 text-sm text-gray-600">
              Container
              <select
                value={container}
                onChange={(e) => setContainer(e.target.value)}
                className="border border-gray-300 bg-white px-2 py-1 text-sm text-gray-900 focus:border-black focus:outline-none"
              >
                {containers.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label className="ml-auto flex items-center gap-2 text-sm text-gray-600">
            <input
              type="checkbox"
              checked={wrap}
              onChange={(e) => setWrap(e.target.checked)}
              className="h-4 w-4 accent-black"
            />
            Wrap
          </label>
        </div>

        {/* Log body */}
        <pre
          ref={scrollRef}
          onScroll={onScroll}
          className={`m-0 flex-1 overflow-auto bg-neutral-900 p-4 font-mono text-xs leading-relaxed text-neutral-100 ${wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre"}`}
        >
          {error && lines.length === 0 ? (
            <span className="text-red-400">{error}</span>
          ) : lines.length === 0 ? (
            <span className="text-neutral-500">
              {status === "live" || ended
                ? "No log output."
                : "Connecting…"}
            </span>
          ) : (
            lines.map((line, i) => <LogLine key={i} line={line} />)
          )}
          {ended && lines.length > 0 && (
            <div className="mt-2 text-neutral-500">— end of logs —</div>
          )}
        </pre>
      </div>
    </div>
  );
}

// Matches the RFC3339(Nano) timestamp the server prefixes each line with when
// timestamps are requested, e.g. "2026-06-01T12:03:47.123456789Z rest…".
const TS_PREFIX = /^(\d{4}-\d{2}-\d{2}T[\d:.]+(?:Z|[+-]\d{2}:\d{2}))\s([\s\S]*)$/;

// LogLine renders one log line, splitting off the leading timestamp (if present)
// into a muted span so it reads as metadata rather than content.
function LogLine({ line }: { line: string }) {
  const m = TS_PREFIX.exec(line);
  if (!m) return <div>{line || " "}</div>;
  const [, ts, rest] = m;
  return (
    <div>
      <span className="text-neutral-500">{ts}</span> {rest || " "}
    </div>
  );
}

function LogStatus({ status, ended }: { status: StreamStatus; ended: boolean }) {
  if (ended) {
    return <span className="text-xs font-medium text-gray-500">Ended</span>;
  }
  const config: Record<StreamStatus, { label: string; dot: string; text: string }> = {
    live: { label: "Streaming", dot: "bg-green-500", text: "text-gray-600" },
    connecting: { label: "Connecting…", dot: "bg-gray-400", text: "text-gray-500" },
    reconnecting: { label: "Reconnecting…", dot: "bg-amber-500", text: "text-amber-700" },
    error: { label: "Disconnected", dot: "bg-red-500", text: "text-red-700" },
  };
  const c = config[status];
  return (
    <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${c.text}`}>
      <span
        className={`h-2 w-2 rounded-full ${c.dot} ${status === "connecting" || status === "reconnecting" ? "animate-pulse" : ""}`}
      />
      {c.label}
    </span>
  );
}
