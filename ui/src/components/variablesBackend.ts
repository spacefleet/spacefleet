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

// VariablesBackend is the read/write seam the editor talks to: apiBackend hits
// the real per-scope endpoints. Each call returns a normalized { data, error }
// so the editor's JSX is identical for every scope.
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
