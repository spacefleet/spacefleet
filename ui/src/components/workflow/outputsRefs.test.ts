import { describe, expect, it } from "vitest";
import {
  componentsReferencing,
  outputsRefSnippet,
  upstreamTofuNames,
  type RefGraphEdge,
  type RefGraphNode,
} from "./outputsRefs";

function comp(id: string, name: string, type: string, parentId?: string): RefGraphNode {
  return { id, type: "component", parentId, data: { name, type } };
}
function group(id: string, name: string): RefGraphNode {
  return { id, type: "group", data: { name } };
}
function edge(source: string, target: string): RefGraphEdge {
  return { source, target };
}

describe("upstreamTofuNames", () => {
  it("lists direct and transitive OpenTofu ancestors, not helm ones or descendants", () => {
    // infra(tf) → mid(helm) → web(helm); db(tf) ← web (downstream of web).
    const nodes = [
      comp("infra", "infra", "terraform"),
      comp("mid", "mid", "helm"),
      comp("web", "web", "helm"),
      comp("db", "db", "terraform"),
    ];
    const edges = [edge("infra", "mid"), edge("mid", "web"), edge("web", "db")];
    expect(upstreamTofuNames("web", nodes, edges)).toEqual(["infra"]);
    expect(upstreamTofuNames("infra", nodes, edges)).toEqual([]);
  });

  it("desugars groups on both ends: depending on a group reaches its members, and a group's deps apply to members", () => {
    // web depends on group g (member: infra/tf); member2 inherits g2's dep on net.
    const nodes = [
      group("g", "platform"),
      comp("infra", "infra", "terraform", "g"),
      comp("web", "web", "helm"),
      group("g2", "apps"),
      comp("member2", "member2", "helm", "g2"),
      comp("net", "net", "terraform"),
    ];
    const edges = [edge("g", "web"), edge("net", "g2")];
    expect(upstreamTofuNames("web", nodes, edges)).toEqual(["infra"]);
    expect(upstreamTofuNames("member2", nodes, edges)).toEqual(["net"]);
  });
});

describe("componentsReferencing", () => {
  function scan(
    name: string,
    config: Record<string, string>,
    target_namespace = "",
  ) {
    return { name, config, target_namespace };
  }
  it("finds references in values, release name, and namespace, tolerating brace whitespace", () => {
    const comps = [
      scan("web", { values: "ns: ${{ components.infra.outputs.namespace }}" }),
      scan("api", { release_name: "x-${{components.infra.outputs.id}}" }),
      scan("jobs", {}, "${{  components.infra.outputs.namespace }}"),
      scan("other", { values: "ns: ${{ components.infra2.outputs.namespace }}" }),
      scan("plain", { values: "replicas: 2" }),
    ];
    expect(componentsReferencing("infra", comps)).toEqual(["web", "api", "jobs"]);
    expect(componentsReferencing("", comps)).toEqual([]);
  });

  it("treats names as literals, not regex", () => {
    const comps = [scan("web", { values: "${{ components.axb.outputs.k }}" })];
    expect(componentsReferencing("a.b", comps)).toEqual([]);
  });
});

describe("outputsRefSnippet", () => {
  it("emits the stub the user completes with a key", () => {
    expect(outputsRefSnippet("infra")).toBe("${{ components.infra.outputs. }}");
  });
});
