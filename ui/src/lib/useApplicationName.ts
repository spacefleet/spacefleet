import { useEffect, useState } from "react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";

// useApplicationName loads an application's display name, for pages under an
// app (its workflow, a run) whose own data doesn't carry it. It is undefined
// until loaded, and stays so if the load fails — callers treat it as optional.
export function useApplicationName(appId: string): string | undefined {
  const { currentOrg } = useOrg();
  const [name, setName] = useState<string>();

  useEffect(() => {
    if (!appId) return;
    let cancelled = false;
    setName(undefined);
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}", {
        params: { path: { id: appId } },
      });
      if (!cancelled) setName(data?.name);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, currentOrg?.id]);

  return name;
}
