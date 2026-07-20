import { describe, expect, it } from "vitest";
import { designDraftFileUrl } from "./preview-url";

describe("designDraftFileUrl", () => {
  it("reuses the signed token for another manifest file", () => {
    expect(designDraftFileUrl(
      "https://preview.example.test/p/signed-token/index.html",
      "scripts/app.js",
    )).toBe("https://preview.example.test/p/signed-token/scripts/app.js");
  });

  it("encodes unicode and spaces in every path segment", () => {
    expect(designDraftFileUrl(
      "https://preview.example.test/p/signed-token/index.html",
      "pages/设计 稿.html",
    )).toBe("https://preview.example.test/p/signed-token/pages/%E8%AE%BE%E8%AE%A1%20%E7%A8%BF.html");
  });

  it("keeps an unexpected URL unchanged", () => {
    const url = "https://preview.example.test/unexpected";
    expect(designDraftFileUrl(url, "app.js")).toBe(url);
  });
});
