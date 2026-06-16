import { api } from "../api/client";
import type { components } from "../api/schema";

type Variable = components["schemas"]["Variable"];

// Scope discriminates the three API surfaces a VariablesEditor drives: group-
// level variables (an application group's path), app-level variables (one
// application's path), and a single component's variables (a nested path). All
// three return the same Variable shape, so only the endpoints differ.
export type VariablesScope =
  | { kind: "group"; groupId: string }
  | { kind: "app"; appId: string }
  | { kind: "component"; appId: string; componentId: string };

// VariablesBackend is the read/write seam the editor talks to. The default
// (apiBackend) hits the real per-scope endpoints; a not-yet-saved component
// swaps in an in-memory one (stagedBackend) so its variables can be authored
// inline and flushed once the component exists server-side. Each call returns a
// normalized { data, error } so the editor's JSX is identical either way.
export interface VariablesBackend {
  list(): Promise<{ data: Variable[]; error: string | null }>;
  create(input: {
    name: string;
    value: string;
    sensitive: boolean;
  }): Promise<{ data: Variable | null; error: string | null }>;
  update(
    id: string,
    value: string,
  ): Promise<{ data: Variable | null; error: string | null }>;
  remove(id: string): Promise<{ error: string | null }>;
}

// apiBackend drives the real per-scope variable endpoints. It's the default
// backend for every persisted scope (group/app/saved component).
export function apiBackend(scope: VariablesScope): VariablesBackend {
  return {
    async list() {
      const res =
        scope.kind === "group"
          ? await api.GET("/api/application-groups/{id}/variables", {
              params: { path: { id: scope.groupId } },
            })
          : scope.kind === "app"
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
      return {
        data: res.data ?? [],
        error: res.error ? (res.error.message ?? "Could not load variables") : null,
      };
    },
    async create(input) {
      const res =
        scope.kind === "group"
          ? await api.POST("/api/application-groups/{id}/variables", {
              params: { path: { id: scope.groupId } },
              body: input,
            })
          : scope.kind === "app"
            ? await api.POST("/api/applications/{id}/variables", {
                params: { path: { id: scope.appId } },
                body: input,
              })
            : await api.POST(
                "/api/applications/{id}/components/{componentId}/variables",
                {
                  params: {
                    path: { id: scope.appId, componentId: scope.componentId },
                  },
                  body: input,
                },
              );
      return {
        data: res.data ?? null,
        error:
          res.error || !res.data
            ? (res.error?.message ?? "Could not add variable")
            : null,
      };
    },
    async update(id, value) {
      const res =
        scope.kind === "group"
          ? await api.PATCH(
              "/api/application-groups/{id}/variables/{variableId}",
              {
                params: { path: { id: scope.groupId, variableId: id } },
                body: { value },
              },
            )
          : scope.kind === "app"
            ? await api.PATCH("/api/applications/{id}/variables/{variableId}", {
                params: { path: { id: scope.appId, variableId: id } },
                body: { value },
              })
            : await api.PATCH(
                "/api/applications/{id}/components/{componentId}/variables/{variableId}",
                {
                  params: {
                    path: {
                      id: scope.appId,
                      componentId: scope.componentId,
                      variableId: id,
                    },
                  },
                  body: { value },
                },
              );
      return {
        data: res.data ?? null,
        error:
          res.error || !res.data
            ? (res.error?.message ?? "Could not update variable")
            : null,
      };
    },
    async remove(id) {
      const res =
        scope.kind === "group"
          ? await api.DELETE(
              "/api/application-groups/{id}/variables/{variableId}",
              {
                params: { path: { id: scope.groupId, variableId: id } },
              },
            )
          : scope.kind === "app"
            ? await api.DELETE(
                "/api/applications/{id}/variables/{variableId}",
                {
                  params: { path: { id: scope.appId, variableId: id } },
                },
              )
            : await api.DELETE(
                "/api/applications/{id}/components/{componentId}/variables/{variableId}",
                {
                  params: {
                    path: {
                      id: scope.appId,
                      componentId: scope.componentId,
                      variableId: id,
                    },
                  },
                },
              );
      return {
        error: res.error ? (res.error.message ?? "Could not delete variable") : null,
      };
    },
  };
}

// stagedBackend is an in-memory VariablesBackend for a not-yet-persisted
// component: add/replace/delete operate on a list held by the caller (the
// workflow draft), so a brand-new component's variables can be authored before
// the component exists server-side. The plaintext value is held on the staged
// row (masked in the UI for a sensitive one) only until the workflow save
// flushes these rows to the real create endpoint.
export function stagedBackend(
  get: () => Variable[],
  set: (vars: Variable[]) => void,
): VariablesBackend {
  return {
    async list() {
      return { data: get(), error: null };
    },
    async create(input) {
      const now = new Date().toISOString();
      const v: Variable = {
        id: crypto.randomUUID(),
        name: input.name,
        sensitive: input.sensitive,
        // The plaintext is kept on the row so the flush can POST it; a sensitive
        // one is masked in the row UI (which keys off `sensitive`, not `value`).
        value: input.value,
        created_at: now,
        updated_at: now,
      };
      set([...get(), v]);
      return { data: v, error: null };
    },
    async update(id, value) {
      const next = get().map((v) =>
        v.id === id ? { ...v, value, updated_at: new Date().toISOString() } : v,
      );
      set(next);
      return { data: next.find((v) => v.id === id) ?? null, error: null };
    },
    async remove(id) {
      set(get().filter((v) => v.id !== id));
      return { error: null };
    },
  };
}
