import { describe, expect, it } from "vitest";
import {
  componentsReferencing,
  outputsRefSnippet,
  upstreamTofuNames,
  type RefStage,
} from "./outputsRefs";

function stage(...comps: [string, string][]): RefStage {
  return { components: comps.map(([name, type]) => ({ id: name, name, type })) };
}

describe("upstreamTofuNames", () => {
  // infra(tf) | mid(helm), net(tf) | web(helm), db(tf)
  const stages = [
    stage(["infra", "terraform"]),
    stage(["mid", "helm"], ["net", "terraform"]),
    stage(["web", "helm"], ["db", "terraform"]),
  ];

  it("lists the OpenTofu components of every earlier stage, sorted", () => {
    expect(upstreamTofuNames("web", stages)).toEqual(["infra", "net"]);
    expect(upstreamTofuNames("mid", stages)).toEqual(["infra"]);
  });

  it("never lists a component of the same or a later stage", () => {
    // net runs alongside mid; db runs after it.
    expect(upstreamTofuNames("mid", stages)).not.toContain("net");
    expect(upstreamTofuNames("infra", stages)).toEqual([]);
  });

  it("returns nothing for a component that isn't in the workflow", () => {
    expect(upstreamTofuNames("ghost", stages)).toEqual([]);
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
