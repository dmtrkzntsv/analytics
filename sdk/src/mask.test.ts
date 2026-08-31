import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveMask } from "./mask";

afterEach(() => {
  vi.restoreAllMocks();
  delete (globalThis as Record<string, unknown>).myMask;
});

describe("resolveMask", () => {
  it("returns undefined when no mask is requested", () => {
    expect(resolveMask(undefined)).toBeUndefined();
    expect(resolveMask("")).toBeUndefined();
  });

  it("resolves the uuid built-in", () => {
    const f = resolveMask("uuid")!;
    expect(f("https://a.example.com/u/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31"))
      .toBe("https://a.example.com/u/[id]");
  });

  it("resolves comma-separated built-ins", () => {
    const f = resolveMask("uuid,numeric")!;
    expect(f("https://a.example.com/account/8812")).toBe("https://a.example.com/account/[id]");
  });

  it("resolves a regexp literal string", () => {
    const f = resolveMask("/\\d+/g")!;
    expect(f("https://a.example.com/account/8812/edit"))
      .toBe("https://a.example.com/account/[id]/edit");
  });

  // Whatever the pattern matches is what [id] replaces -- delimiters
  // included. A pattern written to match the surrounding slashes eats them,
  // which is a real way to produce a path nobody expects.
  it("replaces exactly what the pattern matched, delimiters and all", () => {
    const f = resolveMask("/\\/account\\/\\d+/g")!;
    expect(f("https://a.example.com/account/8812/edit"))
      .toBe("https://a.example.com[id]/edit");
  });

  it("resolves a global function by name", () => {
    (globalThis as Record<string, unknown>).myMask = (href: string) => href + "?masked";
    const f = resolveMask("myMask")!;
    expect(f("https://a.example.com/x")).toBe("https://a.example.com/x?masked");
  });

  it("takes a function directly", () => {
    const f = resolveMask((href: string) => href.toUpperCase())!;
    expect(f("/x")).toBe("/X");
  });

  it("takes a RegExp directly", () => {
    const f = resolveMask(/\d+/g)!;
    expect(f("/a/1/b/2")).toBe("/a/[id]/b/[id]");
  });

  // Fail closed: an unresolvable mask returns null, which the caller turns
  // into "drop pageviews". Falling back to identity would ship the raw path
  // that masking existed to hide.
  it("returns null for a missing global", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(resolveMask("noSuchFunction")).toBeNull();
    expect(warn).toHaveBeenCalledOnce();
  });

  it("returns null for an unparseable regexp", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(resolveMask("/[unclosed/")).toBeNull();
    expect(warn).toHaveBeenCalledOnce();
  });
});
