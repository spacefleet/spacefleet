import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, Trash2, KeyRound } from "lucide-react";
import type { components } from "../api/schema";
import { apiBackend, type VariablesBackend, type VariablesScope } from "./variablesBackend";

type Variable = components["schemas"]["Variable"];

export type { VariablesScope } from "./variablesBackend";

// VariablesEditor lists and edits the variables in a scope. A variable is a
// name/value pair passed to component jobs as an environment variable; a
// sensitive one is sealed server-side and never returned, so its value is shown
// as "set" and can only be replaced (never read back). Used at the group, app,
// and component levels (see VariablesScope). Editors can add/replace/delete;
// viewers see a read-only list. Pass `backend` to override the default API
// transport (the workflow editor passes an in-memory one for a new component).
export function VariablesEditor({
  scope,
  canEdit,
  backend,
}: {
  scope: VariablesScope;
  canEdit: boolean;
  backend?: VariablesBackend;
}) {
  const [vars, setVars] = useState<Variable[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const scopeKey =
    scope.kind === "group"
      ? `group:${scope.groupId}`
      : scope.kind === "app"
        ? `app:${scope.appId}`
        : `component:${scope.appId}:${scope.componentId}`;

  // A provided backend is used as-is; otherwise build the API backend for this
  // scope. Keyed on scopeKey (not the scope object) so it's stable per scope.
  const resolved = useMemo(
    () => backend ?? apiBackend(scope),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [backend, scopeKey],
  );

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const res = await resolved.list();
    if (res.error) setError(res.error);
    setVars(res.data);
    setLoading(false);
  }, [resolved]);

  useEffect(() => {
    void load();
  }, [load]);

  async function onDelete(v: Variable) {
    if (!confirm(`Delete the variable "${v.name}"?`)) return;
    const res = await resolved.remove(v.id);
    if (res.error) {
      setError(res.error);
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
                  backend={resolved}
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
              backend={resolved}
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
  backend,
  canEdit,
  onChanged,
  onDelete,
  onError,
}: {
  variable: Variable;
  backend: VariablesBackend;
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
    const res = await backend.update(variable.id, value);
    setSaving(false);
    if (res.error || !res.data) {
      onError(res.error ?? "Could not update variable");
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
  backend,
  existing,
  onAdded,
  onError,
}: {
  backend: VariablesBackend;
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
    const res = await backend.create({ name: trimmed, sensitive, value });
    setSubmitting(false);
    if (res.error || !res.data) {
      onError(res.error ?? "Could not add variable");
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
