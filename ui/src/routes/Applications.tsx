import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { AppWindow, Folder, FolderPlus, Loader2, Plus } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type Application = components["schemas"]["Application"];
type ApplicationGroup = components["schemas"]["ApplicationGroup"];

// Applications is the Applications › All Apps page: it organizes the org's
// applications like a file directory. Groups (folders) are listed first; click
// one to drill into /applications/groups/:id and see just its apps. Ungrouped
// apps sit below at the root. Each app row links to its detail page, and editors
// can move an app into a group inline. The X-Organization-ID header is attached
// automatically (api/client.ts).
export function Applications() {
  const { currentOrg, currentRole } = useOrg();
  const canEdit = currentRole !== "viewer";
  const navigate = useNavigate();
  const [apps, setApps] = useState<Application[]>([]);
  const [groups, setGroups] = useState<ApplicationGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Inline "new group" form state.
  const [creatingGroup, setCreatingGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState("");
  const [savingGroup, setSavingGroup] = useState(false);
  const [groupError, setGroupError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [appsRes, groupsRes] = await Promise.all([
      api.GET("/api/applications"),
      api.GET("/api/application-groups"),
    ]);
    if (appsRes.error)
      setError(appsRes.error.message ?? "Could not load applications");
    setApps(appsRes.data ?? []);
    setGroups(groupsRes.data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function createGroup() {
    const name = newGroupName.trim();
    if (!name) return;
    setSavingGroup(true);
    setGroupError(null);
    const { error } = await api.POST("/api/application-groups", {
      body: { name },
    });
    if (error) {
      setGroupError(error.message ?? "Could not create the group");
      setSavingGroup(false);
      return;
    }
    setSavingGroup(false);
    setNewGroupName("");
    setCreatingGroup(false);
    void load();
  }

  async function moveApp(appId: string, groupId: string) {
    const { error } = await api.PUT("/api/applications/{id}/group", {
      params: { path: { id: appId } },
      body: { group_id: groupId === "" ? null : groupId },
    });
    if (error) {
      setError(error.message ?? "Could not move the application");
      return;
    }
    void load();
  }

  const ungrouped = apps.filter((a) => !a.group_id);
  const countFor = (groupId: string) =>
    apps.filter((a) => a.group_id === groupId).length;
  const empty = !loading && groups.length === 0 && apps.length === 0;

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Applications
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">All Apps</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Organize applications into groups, or deploy them directly.
          </p>
        </div>
        {canEdit && (
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => {
                setCreatingGroup(true);
                setGroupError(null);
              }}
              className="inline-flex items-center gap-2 border border-neutral-300 px-4 py-2 text-sm font-medium text-neutral-700 hover:bg-neutral-50"
            >
              <FolderPlus className="h-4 w-4" />
              New group
            </button>
            <button
              type="button"
              onClick={() => navigate("/applications/new")}
              className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
            >
              <Plus className="h-4 w-4" />
              Create app
            </button>
          </div>
        )}
      </div>

      {creatingGroup && (
        <div className="mt-6 border border-neutral-200 bg-white p-4">
          <label className="block text-xs font-medium uppercase tracking-wide text-neutral-400">
            New group name
          </label>
          <div className="mt-2 flex items-center gap-2">
            <input
              type="text"
              autoFocus
              value={newGroupName}
              onChange={(e) => setNewGroupName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void createGroup();
                if (e.key === "Escape") setCreatingGroup(false);
              }}
              placeholder="e.g. Backend services"
              maxLength={200}
              className="w-full max-w-sm border border-neutral-300 px-3 py-1.5 text-sm focus:border-neutral-900 focus:outline-none"
            />
            <button
              type="button"
              onClick={() => void createGroup()}
              disabled={savingGroup || newGroupName.trim() === ""}
              className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
            >
              {savingGroup && <Loader2 className="h-4 w-4 animate-spin" />}
              Create
            </button>
            <button
              type="button"
              onClick={() => {
                setCreatingGroup(false);
                setNewGroupName("");
              }}
              className="text-sm text-neutral-500 hover:text-neutral-900"
            >
              Cancel
            </button>
          </div>
          {groupError && (
            <p className="mt-2 text-sm text-red-600">{groupError}</p>
          )}
        </div>
      )}

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : empty ? (
        <div className="mt-6 border border-neutral-200 bg-white p-10 text-center">
          <AppWindow className="mx-auto h-8 w-8 text-neutral-300" />
          <p className="mt-3 text-sm font-medium text-neutral-700">
            No applications yet
          </p>
          <p className="mt-1 text-sm text-neutral-500">
            Create your first application, or a group to organize them.
          </p>
        </div>
      ) : (
        <>
          {/* Groups (folders) */}
          {groups.length > 0 && (
            <div className="mt-6 border border-neutral-200 bg-white">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                    <th className="px-4 py-2 font-medium">Group</th>
                    <th className="px-4 py-2 font-medium">Applications</th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map((g) => (
                    <tr
                      key={g.id}
                      onClick={() => navigate(`/applications/groups/${g.id}`)}
                      className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                    >
                      <td className="px-4 py-3 font-medium text-neutral-900">
                        <span className="inline-flex items-center gap-2">
                          <Folder className="h-4 w-4 text-neutral-400" />
                          {g.name}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {countFor(g.id)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Ungrouped apps (the org root) */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="border-b border-neutral-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-400">
              {groups.length > 0 ? "Ungrouped" : "Applications"}
            </div>
            {ungrouped.length === 0 ? (
              <p className="p-6 text-sm text-neutral-500">
                {groups.length > 0
                  ? "Every application is in a group."
                  : "No applications yet."}
              </p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                    <th className="px-4 py-2 font-medium">Name</th>
                    <th className="px-4 py-2 font-medium">Origin</th>
                    {canEdit && groups.length > 0 && (
                      <th className="px-4 py-2 font-medium">Group</th>
                    )}
                  </tr>
                </thead>
                <tbody>
                  {ungrouped.map((a) => (
                    <tr
                      key={a.id}
                      onClick={() => navigate(`/applications/${a.id}`)}
                      className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                    >
                      <td className="px-4 py-3 font-medium text-neutral-900">
                        {a.name}
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {a.imported ? "Imported" : "Created"}
                      </td>
                      {canEdit && groups.length > 0 && (
                        <td className="px-4 py-3">
                          <select
                            value=""
                            onClick={(e) => e.stopPropagation()}
                            onChange={(e) => {
                              e.stopPropagation();
                              void moveApp(a.id, e.target.value);
                            }}
                            className="border border-neutral-300 bg-white px-2 py-1 text-xs text-neutral-700 focus:border-neutral-900 focus:outline-none"
                          >
                            <option value="">Move to…</option>
                            {groups.map((g) => (
                              <option key={g.id} value={g.id}>
                                {g.name}
                              </option>
                            ))}
                          </select>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}
    </div>
  );
}
