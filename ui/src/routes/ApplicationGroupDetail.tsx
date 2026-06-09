import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  Folder,
  Loader2,
  Pencil,
  Trash2,
  X,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { VariablesEditor } from "../components/VariablesEditor";
import type { components } from "../api/schema";

type Application = components["schemas"]["Application"];
type ApplicationGroup = components["schemas"]["ApplicationGroup"];

// ApplicationGroupDetail is the drill-down for one group (folder), reached by
// clicking a group on the All Apps page (route /applications/groups/:groupId).
// It lists just this group's applications, lets editors rename or delete the
// group, and lets them move an app to another group or back to the org root.
// Deleting a group does not delete its apps — they fall back to the root.
export function ApplicationGroupDetail() {
  const { groupId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [group, setGroup] = useState<ApplicationGroup | null>(null);
  const [groups, setGroups] = useState<ApplicationGroup[]>([]);
  const [apps, setApps] = useState<Application[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Rename state.
  const [editingName, setEditingName] = useState(false);
  const [name, setName] = useState("");
  const [savingName, setSavingName] = useState(false);

  // Delete state.
  const [deleting, setDeleting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [groupRes, appsRes, groupsRes] = await Promise.all([
      api.GET("/api/application-groups/{id}", {
        params: { path: { id: groupId } },
      }),
      api.GET("/api/applications"),
      api.GET("/api/application-groups"),
    ]);
    if (groupRes.error || !groupRes.data) {
      setError(groupRes.error?.message ?? "Could not load this group");
      setGroup(null);
      setLoading(false);
      return;
    }
    setGroup(groupRes.data);
    setName(groupRes.data.name);
    setApps(appsRes.data ?? []);
    setGroups(groupsRes.data ?? []);
    setLoading(false);
  }, [groupId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function saveName() {
    const trimmed = name.trim();
    if (!trimmed || trimmed === group?.name) {
      setEditingName(false);
      setName(group?.name ?? "");
      return;
    }
    setSavingName(true);
    const { error } = await api.PATCH("/api/application-groups/{id}", {
      params: { path: { id: groupId } },
      body: { name: trimmed },
    });
    setSavingName(false);
    if (error) {
      setError(error.message ?? "Could not rename the group");
      return;
    }
    setEditingName(false);
    void load();
  }

  async function runDelete() {
    setDeleting(true);
    const { error } = await api.DELETE("/api/application-groups/{id}", {
      params: { path: { id: groupId } },
    });
    if (error) {
      setError(error.message ?? "Could not delete the group");
      setDeleting(false);
      return;
    }
    navigate("/applications");
  }

  async function moveApp(appId: string, value: string) {
    const { error } = await api.PUT("/api/applications/{id}/group", {
      params: { path: { id: appId } },
      body: { group_id: value === "" ? null : value },
    });
    if (error) {
      setError(error.message ?? "Could not move the application");
      return;
    }
    void load();
  }

  const members = apps.filter((a) => a.group_id === groupId);

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to applications
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error && !group ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : !group ? (
        <p className="mt-6 text-sm text-red-600">Not found</p>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
                Group
              </p>
              {editingName ? (
                <div className="mt-1 flex items-center gap-2">
                  <input
                    type="text"
                    autoFocus
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveName();
                      if (e.key === "Escape") {
                        setEditingName(false);
                        setName(group.name);
                      }
                    }}
                    maxLength={200}
                    className="border border-neutral-300 px-2 py-1 text-2xl font-bold tracking-tight focus:border-neutral-900 focus:outline-none"
                  />
                  <button
                    type="button"
                    onClick={() => void saveName()}
                    disabled={savingName}
                    aria-label="Save name"
                    className="text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
                  >
                    {savingName ? (
                      <Loader2 className="h-5 w-5 animate-spin" />
                    ) : (
                      <Check className="h-5 w-5" />
                    )}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setEditingName(false);
                      setName(group.name);
                    }}
                    aria-label="Cancel rename"
                    className="text-neutral-400 hover:text-neutral-700"
                  >
                    <X className="h-5 w-5" />
                  </button>
                </div>
              ) : (
                <h1 className="mt-1 inline-flex items-center gap-2 break-all text-2xl font-bold tracking-tight">
                  <Folder className="h-5 w-5 text-neutral-400" />
                  {group.name}
                </h1>
              )}
            </div>
            {canEdit && !editingName && (
              <div className="flex flex-wrap items-center justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setEditingName(true)}
                  title="Rename this group"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
                >
                  <Pencil className="h-3.5 w-3.5" />
                  Rename
                </button>
                <button
                  type="button"
                  onClick={() => setConfirmDelete(true)}
                  title="Delete this group"
                  className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  Delete
                </button>
              </div>
            )}
          </div>

          {error && <p className="mt-4 text-sm text-red-600">{error}</p>}

          <div className="mt-6 border border-neutral-200 bg-white">
            {members.length === 0 ? (
              <p className="p-6 text-sm text-neutral-500">
                No applications in this group yet. Move one in from All Apps, or
                from another group.
              </p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                    <th className="px-4 py-2 font-medium">Name</th>
                    <th className="px-4 py-2 font-medium">Namespace</th>
                    <th className="px-4 py-2 font-medium">Origin</th>
                    {canEdit && <th className="px-4 py-2 font-medium">Move</th>}
                  </tr>
                </thead>
                <tbody>
                  {members.map((a) => (
                    <tr
                      key={a.id}
                      onClick={() => navigate(`/applications/${a.id}`)}
                      className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                    >
                      <td className="px-4 py-3 font-medium text-neutral-900">
                        {a.name}
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {a.target_namespace}
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {a.imported ? "Imported" : "Created"}
                      </td>
                      {canEdit && (
                        <td className="px-4 py-3">
                          <select
                            value={groupId}
                            onClick={(e) => e.stopPropagation()}
                            onChange={(e) => {
                              e.stopPropagation();
                              void moveApp(a.id, e.target.value);
                            }}
                            className="border border-neutral-300 bg-white px-2 py-1 text-xs text-neutral-700 focus:border-neutral-900 focus:outline-none"
                          >
                            <option value="">Ungrouped (root)</option>
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

          {/* Variables (group-level: the base env vars for every app in the group) */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Variables
            </h2>
            <p className="mb-3 mt-1 text-xs text-neutral-500">
              Passed to every component job of every application in this group as
              environment variables. These are the lowest priority — an
              application or one of its components can override one of these for
              its own job. A sensitive value is sealed and never shown again.
            </p>
            <VariablesEditor
              scope={{ kind: "group", groupId }}
              canEdit={canEdit}
            />
          </div>

          {confirmDelete && (
            <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4">
              <div className="mt-12 w-full max-w-lg border border-neutral-200 bg-white shadow-lg">
                <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
                  <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
                    <AlertTriangle className="h-5 w-5 text-red-600" />
                    Delete {group.name}
                  </h2>
                  <button
                    type="button"
                    onClick={() => setConfirmDelete(false)}
                    disabled={deleting}
                    className="text-neutral-400 hover:text-neutral-700 disabled:opacity-50"
                    aria-label="Close"
                  >
                    <X className="h-5 w-5" />
                  </button>
                </div>
                <div className="space-y-4 px-5 py-4">
                  <p className="text-sm text-neutral-600">
                    This deletes the group only. Its{" "}
                    {members.length === 1
                      ? "application"
                      : `${members.length} applications`}{" "}
                    are not deleted — they move back to the org root (ungrouped).
                  </p>
                </div>
                <div className="flex items-center justify-end gap-3 border-t border-neutral-200 px-5 py-4">
                  <button
                    type="button"
                    onClick={() => setConfirmDelete(false)}
                    disabled={deleting}
                    className="text-sm text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    onClick={() => void runDelete()}
                    disabled={deleting}
                    className="inline-flex items-center gap-1.5 bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
                  >
                    {deleting && <Loader2 className="h-4 w-4 animate-spin" />}
                    {deleting ? "Deleting…" : "Delete group"}
                  </button>
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
