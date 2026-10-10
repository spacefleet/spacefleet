import { describe, expect, it } from "vitest";
import { attachmentFilename } from "./stateDownload";

describe("attachmentFilename", () => {
  it("reads a quoted or bare filename", () => {
    expect(attachmentFilename('attachment; filename="web-infra.tfstate"')).toBe(
      "web-infra.tfstate",
    );
    expect(attachmentFilename("attachment; filename=web.tfstate")).toBe("web.tfstate");
  });

  it("is null without one", () => {
    expect(attachmentFilename(null)).toBeNull();
    expect(attachmentFilename("attachment")).toBeNull();
  });
});
