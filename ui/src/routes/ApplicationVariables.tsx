import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { VariablesEditor } from "../components/VariablesEditor";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// ApplicationVariables is the application's variables page, reached from the
// detail page's Variables button: the app-level variables every component job
// gets as environment variables. Viewers see the list read-only (the editor
// hides sensitive values and the add/edit controls).
export function ApplicationVariables() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
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
      <button
        type="button"
        onClick={() => navigate(`/applications/${appId}`)}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to application
      </button>
      <h1 className="mt-1 text-xl font-bold tracking-tight">Variables</h1>
      <p className="mt-1 text-sm text-neutral-600">
        Passed to every component job in{" "}
        {appName ? <span className="font-medium">{appName}</span> : "this application"}{" "}
        as environment variables. A component can override one of these for its
        own job. A sensitive value is sealed and never shown again.
      </p>
      <div className="mt-6 border border-neutral-200 bg-white p-4">
        <VariablesEditor scope={{ kind: "app", appId }} canEdit={canEdit} />
      </div>
    </div>
  );
}
