import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Plus, ShieldCheck, Trash2, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { useDocumentTitle } from "../lib/useDocumentTitle";

type Policy = components["schemas"]["Policy"];
type Enforcement = components["schemas"]["PolicyEnforcement"];
type Application = components["schemas"]["Application"];
type TestPlan = components["schemas"]["PolicyTestPlan"];
type TestResult = components["schemas"]["PolicyTestResult"];

const EXAMPLE = `package spacefleet

deny contains msg if {
	some r in input.plan.resources
	r.action == "delete"
	startswith(r.type, "aws_db")
	msg := sprintf("%s would be destroyed", [r.address])
}
`;

// Policies is the Admin › Policies page: Rego rules checked against every
// OpenTofu plan of the applications they cover, before the apply. Admins
// manage them; other members can read them.
export function Policies() {
  const { currentOrg, currentRole } = useOrg();
  useDocumentTitle("Policies");
  const isAdmin = currentRole === "admin";
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [apps, setApps] = useState<Application[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<Policy | "new" | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [{ data, error }, appsRes] = await Promise.all([
      api.GET("/api/policies"),
      api.GET("/api/applications"),
    ]);
    if (error) setError(error.message ?? "Could not load policies");
    setPolicies(data ?? []);
    setApps(appsRes.data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  const appName = (id?: string | null) =>
    id ? (apps.find((a) => a.id === id)?.name ?? "one application") : "all applications";

  async function onDelete(p: Policy) {
    if (!confirm(`Delete the policy "${p.name}"?`)) return;
    const { error } = await api.DELETE("/api/policies/{id}", { params: { path: { id: p.id } } });
    if (error) {
      setError(error.message ?? "Could not delete the policy");
      return;
    }
    setPolicies((ps) => ps.filter((x) => x.id !== p.id));
  }

  async function onToggle(p: Policy) {
    const { data, error } = await api.PATCH("/api/policies/{id}", {
      params: { path: { id: p.id } },
      body: { enabled: !p.enabled },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not update the policy");
      return;
    }
    setPolicies((ps) => ps.map((x) => (x.id === p.id ? data : x)));
  }

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-500">Admin</p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Policies</h1>
          <p className="mt-1 text-sm text-neutral-300">
            Rego rules checked against every OpenTofu plan before it is applied.
            A blocking violation fails the plan and skips the apply; a warning
            is shown at the approval gate.
          </p>
        </div>
        {isAdmin && (
          <button
            type="button"
            onClick={() => setEditing("new")}
            className="inline-flex shrink-0 items-center gap-2 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover"
          >
            <Plus className="h-4 w-4" />
            Add policy
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-800 bg-neutral-900">
        {loading ? (
          <p className="p-6 text-sm text-neutral-400">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-400">{error}</p>
        ) : policies.length === 0 ? (
          <div className="p-10 text-center">
            <ShieldCheck className="mx-auto h-8 w-8 text-neutral-600" />
            <p className="mt-3 text-sm font-medium text-neutral-300">No policies yet</p>
            <p className="mt-1 text-sm text-neutral-400">
              Add one to guard what an apply may change.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Enforcement</th>
                <th className="px-4 py-2 font-medium">Scope</th>
                <th className="px-4 py-2 font-medium">Enabled</th>
                {isAdmin && <th className="px-4 py-2" />}
              </tr>
            </thead>
            <tbody>
              {policies.map((p) => (
                <tr key={p.id} className="border-b border-neutral-800 last:border-0">
                  <td className="px-4 py-3">
                    <button
                      type="button"
                      onClick={() => setEditing(p)}
                      className="font-medium text-neutral-100 underline-offset-2 hover:underline"
                    >
                      {p.name}
                    </button>
                    {p.description && (
                      <p className="text-xs text-neutral-400">{p.description}</p>
                    )}
                  </td>
                  <td className="px-4 py-3 capitalize text-neutral-300">{p.enforcement}</td>
                  <td className="px-4 py-3 text-neutral-300">{appName(p.application_id)}</td>
                  <td className="px-4 py-3 text-neutral-300">
                    {isAdmin ? (
                      <input
                        type="checkbox"
                        aria-label={`Enable ${p.name}`}
                        checked={p.enabled}
                        onChange={() => void onToggle(p)}
                        className="h-3.5 w-3.5 accent-white"
                      />
                    ) : p.enabled ? (
                      "yes"
                    ) : (
                      "no"
                    )}
                  </td>
                  {isAdmin && (
                    <td className="px-4 py-3 text-right">
                      <button
                        type="button"
                        onClick={() => void onDelete(p)}
                        title="Delete this policy"
                        aria-label={`Delete ${p.name}`}
                        className="p-1 text-neutral-500 hover:text-red-400"
                      >
                        <Trash2 className="h-4 w-4" />
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        <PolicyDialog
          policy={editing === "new" ? null : editing}
          apps={apps}
          readOnly={!isAdmin}
          onClose={() => setEditing(null)}
          onSaved={(p) => {
            setPolicies((ps) =>
              ps.some((x) => x.id === p.id) ? ps.map((x) => (x.id === p.id ? p : x)) : [...ps, p],
            );
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

// PolicyDryRun evaluates the Rego as typed against one of the
// organization's recent OpenTofu plans — the way to see what a rule would
// have said before enabling it. Nothing is saved: the result is the
// messages the deny rule produced (or none), an evaluation error, and the
// input document the policy saw, for authors chasing a field name.
function PolicyDryRun({ rego }: { rego: string }) {
  const [plans, setPlans] = useState<TestPlan[]>([]);
  const [planId, setPlanId] = useState("");
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const { data } = await api.GET("/api/policies/plans");
      if (cancelled) return;
      const list = data ?? [];
      setPlans(list);
      setPlanId((cur) => cur || list[0]?.component_run_id || "");
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const run = async () => {
    setTesting(true);
    setError(null);
    setResult(null);
    const { data, error } = await api.POST("/api/policies/test", {
      body: { rego, component_run_id: planId },
    });
    setTesting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not test the policy");
      return;
    }
    setResult(data);
  };

  const label = (p: TestPlan) => {
    const counts = `+${p.add} ~${p.change} -${p.destroy}${p.replace ? ` ±${p.replace}` : ""}`;
    const when = p.finished_at ? new Date(p.finished_at).toLocaleString() : "";
    return `${p.application_name} / ${p.component_name} · ${p.action} · ${counts}${when ? ` · ${when}` : ""}`;
  };

  return (
    <div className="border border-neutral-800 bg-neutral-800/50 p-3">
      <p className="text-xs font-medium text-neutral-300">Try it against a recent plan</p>
      {plans.length === 0 ? (
        <p className="mt-1 text-xs text-neutral-400">
          No OpenTofu plans have run yet — once one has, you can check what this
          policy would have said about it here.
        </p>
      ) : (
        <div className="mt-2 flex flex-wrap items-end gap-2">
          <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs text-neutral-300">
            Plan
            <select
              aria-label="Plan to test against"
              value={planId}
              onChange={(e) => {
                setPlanId(e.target.value);
                setResult(null);
              }}
              className="min-w-0 border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
            >
              {plans.map((p) => (
                <option key={p.component_run_id} value={p.component_run_id}>
                  {label(p)}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={() => void run()}
            disabled={testing || rego.trim() === "" || planId === ""}
            className="border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
          >
            {testing ? "Testing…" : "Test policy"}
          </button>
        </div>
      )}
      {error && <p className="mt-2 whitespace-pre-wrap text-xs text-red-400">{error}</p>}
      {result && (
        <div className="mt-2 text-sm">
          {result.error ? (
            <p className="text-red-300">
              <span className="font-medium">Evaluation error</span> — a block policy would
              fail the plan: <span className="font-mono text-xs">{result.error}</span>
            </p>
          ) : result.violations.length === 0 ? (
            <p className="text-emerald-300">No violations — this plan would pass.</p>
          ) : (
            <div className="text-red-300">
              <p className="font-medium">
                {result.violations.length} violation{result.violations.length === 1 ? "" : "s"}
              </p>
              <ul className="mt-1 list-disc pl-5 font-mono text-xs">
                {result.violations.map((v, i) => (
                  <li key={i}>{v}</li>
                ))}
              </ul>
            </div>
          )}
          <details className="mt-2 text-xs text-neutral-300">
            <summary className="cursor-pointer">What the policy saw</summary>
            <pre className="mt-1 max-h-64 overflow-auto border border-neutral-800 bg-neutral-900 p-2 font-mono text-[11px] leading-relaxed">
              {JSON.stringify(result.input, null, 2)}
            </pre>
          </details>
        </div>
      )}
    </div>
  );
}

// PolicyDialog creates or edits a policy: name, description, Rego source,
// enforcement, and an optional application. The Rego is compiled
// server-side on save; a compiler error is shown beneath the editor.
function PolicyDialog({
  policy,
  apps,
  readOnly,
  onClose,
  onSaved,
}: {
  policy: Policy | null;
  apps: Application[];
  readOnly: boolean;
  onClose: () => void;
  onSaved: (p: Policy) => void;
}) {
  const [name, setName] = useState(policy?.name ?? "");
  const [description, setDescription] = useState(policy?.description ?? "");
  const [rego, setRego] = useState(policy?.rego ?? EXAMPLE);
  const [enforcement, setEnforcement] = useState<Enforcement>(policy?.enforcement ?? "block");
  const [appId, setAppId] = useState(policy?.application_id ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    const res = policy
      ? await api.PATCH("/api/policies/{id}", {
          params: { path: { id: policy.id } },
          body: { name: name.trim(), description, rego, enforcement, application_id: appId },
        })
      : await api.POST("/api/policies", {
          body: {
            name: name.trim(),
            description,
            rego,
            enforcement,
            ...(appId ? { application_id: appId } : {}),
          },
        });
    setSubmitting(false);
    if (res.error || !res.data) {
      setError(res.error?.message ?? "Could not save the policy");
      return;
    }
    onSaved(res.data);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <form
        onSubmit={(e) => void submit(e)}
        className="flex max-h-full w-full max-w-2xl flex-col border border-neutral-800 bg-neutral-900 p-6"
      >
        <div className="flex items-start justify-between">
          <h2 className="text-lg font-semibold">{policy ? policy.name : "Add policy"}</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="p-1 text-neutral-500 hover:text-neutral-100">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="mt-4 grid min-h-0 flex-1 gap-4 overflow-auto">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs text-neutral-300">
              Name
              <input
                type="text"
                aria-label="Name"
                value={name}
                readOnly={readOnly}
                onChange={(e) => setName(e.target.value)}
                className="border border-neutral-700 px-2 py-1.5 text-sm text-neutral-100"
              />
            </label>
            <label className="flex flex-col gap-1 text-xs text-neutral-300">
              Description
              <input
                type="text"
                aria-label="Description"
                value={description}
                readOnly={readOnly}
                onChange={(e) => setDescription(e.target.value)}
                className="border border-neutral-700 px-2 py-1.5 text-sm text-neutral-100"
              />
            </label>
            <label className="flex flex-col gap-1 text-xs text-neutral-300">
              Enforcement
              <select
                aria-label="Enforcement"
                value={enforcement}
                disabled={readOnly}
                onChange={(e) => setEnforcement(e.target.value as Enforcement)}
                className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
              >
                <option value="block">Block — fail the plan, skip the apply</option>
                <option value="warn">Warn — show at the approval gate</option>
              </select>
            </label>
            <label className="flex flex-col gap-1 text-xs text-neutral-300">
              Application
              <select
                aria-label="Application"
                value={appId}
                disabled={readOnly}
                onChange={(e) => setAppId(e.target.value)}
                className="border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
              >
                <option value="">All applications</option>
                {apps.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <label className="flex flex-col gap-1 text-xs text-neutral-300">
            Rego (package spacefleet, a deny rule)
            <textarea
              aria-label="Rego"
              value={rego}
              readOnly={readOnly}
              rows={14}
              spellCheck={false}
              onChange={(e) => setRego(e.target.value)}
              className="border border-neutral-700 px-2 py-1.5 font-mono text-xs leading-relaxed text-neutral-100"
            />
          </label>
          {!readOnly && <PolicyDryRun rego={rego} />}
        </div>
        {error && <p className="mt-3 whitespace-pre-wrap text-sm text-red-400">{error}</p>}
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
          >
            {readOnly ? "Close" : "Cancel"}
          </button>
          {!readOnly && (
            <button
              type="submit"
              disabled={submitting || name.trim() === "" || rego.trim() === ""}
              className="bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
            >
              {submitting ? "Saving…" : "Save policy"}
            </button>
          )}
        </div>
      </form>
    </div>
  );
}
