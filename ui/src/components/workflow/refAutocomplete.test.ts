import { describe, expect, it } from "vitest";
import {
  applySuggestion,
  findActiveRef,
  suggestionsFor,
  type RefContext,
} from "./refAutocomplete";

const ctx: RefContext = {
  varsNames: ["LOG_LEVEL", "REGION"],
  componentNames: ["infra", "helm release"],
  outputKeysByName: {
    infra: [
      { key: "vpc_id", sensitive: false, type: '"string"' },
      { key: "db_password", sensitive: true },
    ],
  },
};

const labels = (inner: string) => suggestionsFor(inner, ctx).map((s) => s.label);

describe("findActiveRef", () => {
  it("returns null when the caret isn't inside a reference", () => {
    expect(findActiveRef("hello world", 5)).toBeNull();
  });

  it("opens as soon as the caret is just past ${{", () => {
    const r = findActiveRef("${{ ", 4);
    expect(r).toEqual({ start: 0, inner: " " });
  });

  it("captures the inner text typed so far", () => {
    const text = "image: ${{ vars.RE";
    expect(findActiveRef(text, text.length)).toEqual({
      start: 7,
      inner: " vars.RE",
    });
  });

  it("is closed once a }} precedes the caret", () => {
    const text = "${{ run.id }} more";
    expect(findActiveRef(text, text.length)).toBeNull();
  });

  it("tracks the second, still-open reference after a closed one", () => {
    const text = "${{ run.id }} ${{ vars.";
    expect(findActiveRef(text, text.length)).toEqual({
      start: 14,
      inner: " vars.",
    });
  });

  it("does not treat an escaped $${{ as an opener", () => {
    const text = "$${{ vars";
    expect(findActiveRef(text, text.length)).toBeNull();
  });
});

describe("suggestionsFor", () => {
  it("offers the three namespaces inside an empty reference", () => {
    expect(labels(" ")).toEqual(["vars", "run", "components"]);
  });

  it("filters namespaces by prefix", () => {
    expect(labels("va")).toEqual(["vars"]);
  });

  it("suggests variable names after vars.", () => {
    expect(labels("vars.")).toEqual(["LOG_LEVEL", "REGION"]);
    expect(labels("vars.RE")).toEqual(["REGION"]);
  });

  it("hints when no variables exist", () => {
    const s = suggestionsFor("vars.", { ...ctx, varsNames: [] });
    expect(s).toHaveLength(1);
    expect(s[0].disabled).toBe(true);
  });

  it("suggests run keys, filtered by prefix", () => {
    expect(labels("run.git")).toEqual(["git_ref", "git_sha", "git_sha_short"]);
  });

  it("suggests upstream component names and jumps to their .outputs.", () => {
    expect(labels("components.")).toEqual(["infra", "helm release"]);
    const s = suggestionsFor("components.inf", ctx);
    expect(s[0].replacement).toBe("components.infra.outputs.");
    expect(s[0].terminal).toBeFalsy();
  });

  it("hints when the node has no upstream OpenTofu dependency", () => {
    const s = suggestionsFor("components.", { ...ctx, componentNames: [] });
    expect(s[0].disabled).toBe(true);
  });

  it("suggests known output keys, terminal, with sensitivity/type detail", () => {
    const s = suggestionsFor("components.infra.outputs.", ctx);
    expect(s.map((x) => x.label)).toEqual(["vpc_id", "db_password"]);
    expect(s[0]).toMatchObject({
      replacement: "components.infra.outputs.vpc_id",
      terminal: true,
      detail: '"string"',
    });
    expect(s[1].detail).toBe("sensitive");
  });

  it("degrades to a hint when a component's output keys aren't known yet", () => {
    const s = suggestionsFor("components.helm release.outputs.", ctx);
    expect(s).toHaveLength(1);
    expect(s[0].disabled).toBe(true);
  });

  it("returns nothing for an unknown namespace", () => {
    expect(suggestionsFor("foo.bar", ctx)).toEqual([]);
  });
});

describe("applySuggestion", () => {
  it("splices a non-terminal suggestion and leaves the caret ready", () => {
    const r = applySuggestion("${{ ", 0, 4, {
      replacement: "vars.",
      label: "vars",
    });
    expect(r.text).toBe("${{ vars.");
    expect(r.caret).toBe(r.text.length);
  });

  it("closes the reference for a terminal suggestion", () => {
    const r = applySuggestion("img: ${{ vars.RE", 5, 16, {
      replacement: "vars.REGION",
      label: "REGION",
      terminal: true,
    });
    expect(r.text).toBe("img: ${{ vars.REGION }}");
    expect(r.caret).toBe(r.text.length);
  });

  it("absorbs a closing brace the author already typed", () => {
    // caret sits right after "vars." with " }}" already present.
    const r = applySuggestion("${{ vars. }}", 0, 9, {
      replacement: "vars.REGION",
      label: "REGION",
      terminal: true,
    });
    expect(r.text).toBe("${{ vars.REGION }}");
  });

  it("never rewrites text after the caret", () => {
    const r = applySuggestion("${{ va trailing", 0, 6, {
      replacement: "vars.",
      label: "vars",
    });
    expect(r.text).toBe("${{ vars. trailing");
  });
});
