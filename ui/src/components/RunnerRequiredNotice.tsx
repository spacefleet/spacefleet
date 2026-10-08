import { Link } from "react-router";
import { ArrowRight, Server } from "lucide-react";
import { cn } from "../lib/utils";
import type { components } from "../api/schema";

type Cluster = components["schemas"]["Cluster"];

// RunnerRequiredNotice explains why an application can't be created yet: the
// org has no cluster to run its deploy jobs on — either none registered at
// all, or none set up as a runner (the server rejects a runner cluster that
// isn't one). It links to the clusters page, where the user
// fixes that — straight into the register dialog when there are none.
export function RunnerRequiredNotice({
  clusters,
  className,
}: {
  clusters: Cluster[];
  className?: string;
}) {
  const none = clusters.length === 0;
  return (
    <div
      role="status"
      className={cn("border border-amber-200 bg-amber-50 p-4", className)}
    >
      <div className="flex items-start gap-3">
        <Server className="mt-0.5 h-4 w-4 shrink-0 text-amber-700" />
        <div>
          <p className="text-sm font-medium text-amber-900">
            {none
              ? "Register a cluster to create applications"
              : "Set up a runner to create applications"}
          </p>
          <p className="mt-1 text-sm text-amber-800">
            {none
              ? "An application runs its deploy jobs on a runner cluster, and this organization hasn't registered one yet. Register a Kubernetes cluster and set it up as a runner, then come back to create your application."
              : "An application runs its deploy jobs on a runner cluster, but none of this organization's clusters is set up as one yet. Open a cluster and set it up as a runner, then come back to create your application."}
          </p>
          <Link
            to={none ? "/admin/clusters?register=1" : "/admin/clusters"}
            className="mt-3 inline-flex items-center gap-1.5 text-sm font-medium text-amber-900 underline-offset-2 hover:underline"
          >
            {none ? "Register a cluster" : "Go to clusters"}
            <ArrowRight className="h-3.5 w-3.5" />
          </Link>
        </div>
      </div>
    </div>
  );
}
