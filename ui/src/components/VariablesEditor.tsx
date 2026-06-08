import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, KeyRound } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";

type Variable = components["schemas"]["Variable"];

// Scope discriminates the two API surfaces a VariablesEditor drives: app-level
// variables (one path) and a single component's variables (a nested path). Both
// return the same Variable shape, so only the endpoints differ.
export type VariablesScope =
  | { kind: "app"; appId: string }
  | { kind: "component"; appId: string; componentId: string };

// VariablesEditor lists and edits the variables in a scope. A variable is a
// name/value pair passed to component jobs as an environment variable; a
// sensitive one is sealed server-side and never returned, so its value is shown
// as "set" and can only be replaced (never read back). Used at both the app and
// component levels (see VariablesScope). Editors can add/replace/delete; viewers
// see a read-only list.
export function VariablesEditor({
  scope,
  canEdit,
}: {
  scope: VariablesScope;
  canEdit: boolean;
}) {
  const [vars, setVars] = useState<Variable[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const scopeKey =
    scope.kind === "app"
      ? `app:${scope.appId}`
      : `component:${scope.appId}:${scope.componentId}`;

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const res =
      scope.kind === "app"
        ? await api.GET("/api/applications/{id}/variables", {
            params: { path: { id: scope.appId } },
          })
        : await api.GET(
            "/api/applications/{id}/components/{componentId}/variables",
            {
              params: {
                path: { id: scope.appId, componentId: scope.componentId },
              },
            },
          );
    if (res.error) setError(res.error.message ?? "Could not load variables");
    setVars(res.data ?? []);
    setLoading(false);
    // scopeKey captures the identifying fields; depending on it (not the object
    // identity) reloads only when the scope actually changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeKey]);

  useEffect(() => {
    void load();
  }, [load]);

  async function onDelete(v: Variable) {
    if (!confirm(`Delete the variable "${v.name}"?`)) return;
    const res =
      scope.kind === "app"
        ? await api.DELETE("/api/applications/{id}/variables/{variableId}", {
            params: { path: { id: scope.appId, variableId: v.id } },
          })
        : await api.DELETE(
            "/api/applications/{id}/components/{componentId}/variables/{variableId}",
            {
              params: {
                path: {
                  id: scope.appId,
                  componentId: scope.componentId,
                  variableId: v.id,
                },
              },
            },
          );
    if (res.error) {
      setError(res.error.message ?? "Could not delete variable");
      return;
    }
    setVars((vs) => vs.filter((x) => x.id !== v.id));
  }

  return (
    <div>
      {loading ? (
        <p className="text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="text-sm text-red-600">{error}</p>
      ) : (
        <>
          {vars.length === 0 ? (
            <p className="text-sm text-neutral-500">No variables.</p>
          ) : (
            <ul className="divide-y divide-neutral-100 border border-neutral-200">
              {vars.map((v) => (
                <VariableRow
                  key={v.id}
                  variable={v}
                  scope={scope}
                  canEdit={canEdit}
                  onChanged={(next) =>
                    setVars((vs) =>
                      vs.map((x) => (x.id === next.id ? next : x)),
                    )
                  }
                  onDelete={() => void onDelete(v)}
                  onError={setError}
                />
              ))}
            </ul>
          )}

          {canEdit && (
            <AddVariableForm
              scope={scope}
              existing={vars}
              onAdded={(v) => setVars((vs) => [...vs, v])}
              onError={setError}
            />
          )}
        </>
      )}
    </div>
  );
}

// VariableRow renders one variable: its name, a sensitive badge or the plaintext
// value, and (for editors) a control to replace the value and to delete it.
function VariableRow({
  variable,
  scope,
  canEdit,
  onChanged,
  onDelete,
  onError,
}: {
  variable: Variable;
  scope: VariablesScope;
  canEdit: boolean;
  onChanged: (v: Variable) => void;
  onDelete: () => void;
  onError: (msg: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(variable.value ?? "");
  const [saving, setSaving] = useState(false);

  async function save() {
    setSaving(true);
    const res =
      scope.kind === "app"
        ? await api.PATCH("/api/applications/{id}/variables/{variableId}", {
            params: { path: { id: scope.appId, variableId: variable.id } },
            body: { value },
          })
        : await api.PATCH(
            "/api/applications/{id}/components/{componentId}/variables/{variableId}",
            {
              params: {
                path: {
                  id: scope.appId,
                  componentId: scope.componentId,
                  variableId: variable.id,
                },
              },
              body: { value },
            },
          );
    setSaving(false);
    if (res.error || !res.data) {
      onError(res.error?.message ?? "Could not update variable");
      return;
    }
    onChanged(res.data);
    setEditing(false);
    setValue(res.data.value ?? "");
  }

  return (
    <li className="flex items-center gap-3 px-3 py-2 text-sm">
      <code className="font-mono text-neutral-900">{variable.name}</code>
      {variable.sensitive && (
        <span className="inline-flex items-center gap-1 border border-neutral-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-neutral-500">
          <KeyRound className="h-3 w-3" />
          sensitive
        </span>
      )}
      <span className="min-w-0 flex-1 truncate text-neutral-500">
        {editing ? (
          <input
            type={variable.sensitive ? "password" : "text"}
            autoFocus
            className="w-full border border-neutral-300 px-2 py-1 text-sm"
            placeholder={variable.sensitive ? "enter a new value" : "value"}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        ) : variable.sensitive ? (
          <span className="text-neutral-400">•••••••• (set)</span>
        ) : (
          <span className="font-mono">{variable.value || '""'}</span>
        )}
      </span>
      {canEdit &&
        (editing ? (
          <span className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={() => {
                setEditing(false);
                setValue(variable.value ?? "");
              }}
              className="text-xs text-neutral-500 hover:text-neutral-800"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void save()}
              disabled={saving || (variable.sensitive && value === "")}
              className="bg-black px-2.5 py-1 text-xs font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </span>
        ) : (
          <span className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={() => setEditing(true)}
              className="text-xs text-neutral-600 hover:text-black"
            >
              {variable.sensitive ? "Replace" : "Edit"}
            </button>
            <button
              type="button"
              onClick={onDelete}
              aria-label={`Delete ${variable.name}`}
              className="text-neutral-400 hover:text-red-600"
            >
              <Trash2 className="h-4 w-4" />
            </button>
          </span>
        ))}
    </li>
  );
}

// AddVariableForm is the inline add row: a name, a value, and a sensitive
// toggle. A sensitive value is sealed server-side on save and never returned.
function AddVariableForm({
  scope,
  existing,
  onAdded,
  onError,
}: {
  scope: VariablesScope;
  existing: Variable[];
  onAdded: (v: Variable) => void;
  onError: (msg: string) => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [sensitive, setSensitive] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const trimmed = name.trim();
  const duplicate = existing.some((v) => v.name === trimmed);
  const ready =
    trimmed !== "" && !duplicate && (!sensitive || value !== "") && !submitting;

  async function submit() {
    setSubmitting(true);
    const body = { name: trimmed, sensitive, value };
    const res =
      scope.kind === "app"
        ? await api.POST("/api/applications/{id}/variables", {
            params: { path: { id: scope.appId } },
            body,
          })
        : await api.POST(
            "/api/applications/{id}/components/{componentId}/variables",
            {
              params: {
                path: { id: scope.appId, componentId: scope.componentId },
              },
              body,
            },
          );
    setSubmitting(false);
    if (res.error || !res.data) {
      onError(res.error?.message ?? "Could not add variable");
      return;
    }
    onAdded(res.data);
    setName("");
    setValue("");
    setSensitive(false);
  }

  return (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <input
        aria-label="Variable name"
        className="w-40 border border-neutral-300 px-2 py-1.5 font-mono text-sm"
        placeholder="NAME"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <input
        aria-label="Variable value"
        type={sensitive ? "password" : "text"}
        className="min-w-0 flex-1 border border-neutral-300 px-2 py-1.5 text-sm"
        placeholder="value"
        value={value}
        onChange={(e) => setValue(e.target.value)}
      />
      <label className="inline-flex items-center gap-1.5 text-sm text-neutral-600">
        <input
          type="checkbox"
          className="h-4 w-4 accent-black"
          checked={sensitive}
          onChange={(e) => setSensitive(e.target.checked)}
        />
        Sensitive
      </label>
      <button
        type="button"
        onClick={() => void submit()}
        disabled={!ready}
        className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
      >
        <Plus className="h-3.5 w-3.5" />
        Add
      </button>
      {duplicate && (
        <span className="w-full text-xs text-red-600">
          A variable named “{trimmed}” already exists.
        </span>
      )}
    </div>
  );
}
