import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import { Outlet } from "react-router";
import { api, setOrgProvider } from "../api/client";
import type { components } from "../api/schema";

type Organization = components["schemas"]["Organization"];
type OrgMembership = components["schemas"]["OrgMembership"];

// Where the selected organization id is persisted across reloads. The server
// is the source of truth for *which* orgs you belong to; this only remembers
// which one you last had active.
const STORAGE_KEY = "spacefleet.currentOrgId";

type OrgContextValue = {
  loading: boolean;
  memberships: OrgMembership[];
  currentOrg: Organization | null;
  currentRole: OrgMembership["role"] | null;
  setCurrentOrg: (id: string) => void;
  // refresh re-fetches /api/me and returns the fresh memberships, so callers
  // (e.g. the create-org flow) can act on the result immediately.
  refresh: () => Promise<OrgMembership[]>;
};

const OrgContext = createContext<OrgContextValue | null>(null);

// useOrg reads the organization context. Throws if used outside OrgProvider,
// which only happens through a wiring mistake. Co-located with the provider by
// design (the fast-refresh warning is acceptable for this small context file).
// eslint-disable-next-line react-refresh/only-export-components
export function useOrg(): OrgContextValue {
  const ctx = useContext(OrgContext);
  if (!ctx) throw new Error("useOrg must be used within an OrgProvider");
  return ctx;
}

// OrgProvider loads the current user's organization memberships and tracks the
// selected one. It's a routed element (renders <Outlet />), mounted inside the
// AuthGate so the bearer token is available for its /api/me fetch.
export function OrgProvider() {
  const [memberships, setMemberships] = useState<OrgMembership[]>([]);
  const [loading, setLoading] = useState(true);
  const [currentOrgId, setCurrentOrgId] = useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY),
  );

  // Keep the API client's X-Organization-ID in sync. Set during render (not in
  // an effect) so it's in place before any descendant fires its first request.
  setOrgProvider(() => currentOrgId);

  const refresh = useCallback(async () => {
    const { data, error } = await api.GET("/api/me");
    const next = error || !data ? [] : data.organizations;
    setMemberships(next);
    return next;
  }, []);

  useEffect(() => {
    let active = true;
    setLoading(true);
    void refresh().finally(() => {
      if (active) setLoading(false);
    });
    return () => {
      active = false;
    };
  }, [refresh]);

  // Reconcile the selected org against what we actually belong to: if nothing
  // is selected, or the stored id is stale (e.g. removed elsewhere), fall back
  // to the first membership.
  useEffect(() => {
    if (loading) return;
    const ids = memberships.map((m) => m.organization.id);
    if (currentOrgId && ids.includes(currentOrgId)) return;
    const next = ids[0] ?? null;
    setCurrentOrgId(next);
    if (next) localStorage.setItem(STORAGE_KEY, next);
    else localStorage.removeItem(STORAGE_KEY);
  }, [loading, memberships, currentOrgId]);

  const setCurrentOrg = useCallback((id: string) => {
    setCurrentOrgId(id);
    localStorage.setItem(STORAGE_KEY, id);
  }, []);

  const active = memberships.find((m) => m.organization.id === currentOrgId);

  return (
    <OrgContext.Provider
      value={{
        loading,
        memberships,
        currentOrg: active?.organization ?? null,
        currentRole: active?.role ?? null,
        setCurrentOrg,
        refresh,
      }}
    >
      <Outlet />
    </OrgContext.Provider>
  );
}
