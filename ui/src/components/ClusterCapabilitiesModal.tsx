import { useEffect } from "react";
import { X } from "lucide-react";
import { ClusterCapabilities } from "./ClusterCapabilities";

interface Props {
  clusterId: string;
  clusterName?: string;
  onClose: () => void;
}

// ClusterCapabilitiesModal presents the capability report as a dedicated screen:
// a focused overlay the operator reviews after connecting a cluster (or on
// demand) to see what the cluster's credentials can and can't do, and how to
// grant anything missing. The chrome (title + close) lives here; the body is the
// reusable ClusterCapabilities report, rendered borderless so it sits flush.
export function ClusterCapabilitiesModal({
  clusterId,
  clusterName,
  onClose,
}: Props) {
  // Close on Escape, mirroring PodLogsModal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-2xl flex-col border border-neutral-200 bg-white shadow-xl"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Cluster capabilities"
      >
        <div className="flex items-start justify-between gap-4 border-b border-neutral-200 px-4 py-3">
          <div className="min-w-0">
            <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
              Cluster access{clusterName ? ` · ${clusterName}` : ""}
            </p>
            <h2 className="truncate text-lg font-bold tracking-tight">
              Capabilities
            </h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close capabilities"
            className="shrink-0 p-1 text-neutral-400 hover:text-neutral-900"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="overflow-auto">
          <ClusterCapabilities clusterId={clusterId} bordered={false} />
        </div>
      </div>
    </div>
  );
}
