import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { Breadcrumbs } from "../components/Breadcrumbs";
import { VariablesEditor } from "../components/VariablesEditor";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// ApplicationVariables is the application's variables page, reached from the
// detail page's Variables button: the app-level variables every component job
// gets as environment variables. Viewers see the list read-only (the editor
// hides sensitive values and the add/edit controls).
export function ApplicationVariables() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const canEdit = currentRole !== "viewer";

  // The app's name is only for the title; the editor loads its own rows.
  const [appName, setAppName] = useState<string | null>(null);
  useDocumentTitle("Variables", appName);
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}", {
        params: { path: { id: appId } },
      });
      setAppName(data?.name ?? null);
    })();
  }, [appId, currentOrg?.id]);

  return (
    <div>
      <Breadcrumbs
        items={[
          { label: "Applications", to: "/applications" },
          { label: appName ?? "…", to: `/applications/${appId}` },
        ]}
      />
      <h1 className="mt-2 text-xl font-bold tracking-tight">Variables</h1>
      <p className="mt-1 text-sm text-neutral-300">
        Passed to every component job in{" "}
        {appName ? <span className="font-medium">{appName}</span> : "this application"}{" "}
        as environment variables. A component can override one of these for its
        own job. A sensitive value is sealed and never shown again.
      </p>
      <div className="mt-6 border border-neutral-800 bg-neutral-900 p-4">
        <VariablesEditor scope={{ kind: "app", appId }} canEdit={canEdit} />
      </div>
    </div>
  );
}
