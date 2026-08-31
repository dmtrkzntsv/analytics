# SDK Masking and Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit `$host`/`$path`/`$utm_*` instead of `$url`, and let a site rewrite the URL before it is sent — via `data-mask-url` on the tag, `maskUrl` in `init()`, or a `page()` listener.

**Architecture:** `page()` reads UTMs off the original href, runs the resolved mask over `location.href`, splits the result into `$host`/`$path`, then threads those values through the listener chain. `data-mask-url` registers as the first listener inside `init()`, before the first pageview, so the entry page is covered with no timing rules. Masking fails closed: a broken mask drops pageviews rather than sending unmasked ones.

**Tech Stack:** TypeScript 5.9, esbuild 0.25 (IIFE bundle), vitest 3.2 + jsdom.

**Spec:** `docs/superpowers/specs/2026-08-30-host-path-split-design.md` (§4)

## Global Constraints

- **`npm run build` rewrites the committed bundle** at `internal/server/twillingate.js`. CI fails on any diff, so every source change must ship with a fresh build in the same commit (`sdk/README.md`).
- **Fail closed, loudly.** Any mask failure ⇒ one `console.warn` and **drop pageviews**. Never fall back to the unmasked path (spec §4.5).
- **Every `data-*` attribute has an `init()` equivalent** with identical semantics; a string `maskUrl` resolves through the *same* code path as the attribute, not a second implementation (spec §4.2).
- **Dedup keys on the raw path**, never the masked one — otherwise `/account/1` → `/account/2` collapses to one pageview (spec §4.3).
- **`$path` carries no query string by default**, in either routing mode. `util.withQuery` is the explicit opt-out (spec §4.6, §4.7).
- **Commit type:** `feat(sdk)`. `CLAUDE.md` does not yet list `sdk` as a scope — Task 6 adds it.
- Run `npm test` and `npm run typecheck` from `sdk/` as the gate.

---

### Task 1: `util.maskIds` and `util.withQuery`

**Files:**
- Create: `sdk/src/util.ts`
- Create: `sdk/src/util.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `maskIds(value: string, opts?: {numeric?: boolean; hex?: boolean}): string`
  - `withQuery(path: string, url: string, keys: string[]): string`

- [ ] **Step 1: Write the failing test**

`sdk/src/util.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { maskIds, withQuery } from "./util";

describe("maskIds", () => {
  it("masks uuid segments", () => {
    expect(maskIds("/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit"))
      .toBe("/account/[id]/edit");
  });

  it("masks uppercase uuids", () => {
    expect(maskIds("/u/3F8A91C2-4B7E-4D1A-9F2C-8E6B5A0D7C31"))
      .toBe("/u/[id]");
  });

  it("masks ulid segments", () => {
    expect(maskIds("/o/01ARZ3NDEKTSV4RRFFQ69G5FAV")).toBe("/o/[id]");
  });

  // /2024/annual-report is a real path; masking digits by default would
  // silently mangle it, so numeric is opt-in.
  it("leaves numeric segments alone by default", () => {
    expect(maskIds("/2024/annual-report")).toBe("/2024/annual-report");
  });

  it("masks numeric segments when asked", () => {
    expect(maskIds("/account/8812/edit", { numeric: true }))
      .toBe("/account/[id]/edit");
  });

  it("masks long hex blobs when asked", () => {
    expect(maskIds("/o/507f1f77bcf86cd799439011", { hex: true }))
      .toBe("/o/[id]");
  });

  // The host must never be treated as a segment.
  it("leaves the host alone in an absolute url", () => {
    expect(maskIds("https://shop.example.com/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31"))
      .toBe("https://shop.example.com/account/[id]");
  });

  it("masks hash segments so hash routes are covered", () => {
    expect(maskIds("https://a.example.com/app/#/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit"))
      .toBe("https://a.example.com/app/#/account/[id]/edit");
  });

  it("preserves the query string", () => {
    expect(maskIds("https://a.example.com/x?utm_source=news"))
      .toBe("https://a.example.com/x?utm_source=news");
  });
});

describe("withQuery", () => {
  it("appends only allowlisted params", () => {
    expect(withQuery("/settings", "https://a.example.com/settings?tab=billing&secret=1", ["tab"]))
      .toBe("/settings?tab=billing");
  });

  // Sorting is the whole reason this ships as a helper: unsorted params
  // would put ?a=1&b=2 and ?b=2&a=1 in two different rows.
  it("sorts params by key", () => {
    expect(withQuery("/s", "https://a.example.com/s?b=2&a=1", ["a", "b"]))
      .toBe("/s?a=1&b=2");
  });

  it("returns the path unchanged when nothing matches", () => {
    expect(withQuery("/s", "https://a.example.com/s?x=1", ["tab"])).toBe("/s");
  });

  it("ignores an unparseable url", () => {
    expect(withQuery("/s", "not a url", ["tab"])).toBe("/s");
  });
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd sdk && npx vitest run src/util.test.ts`
Expected: FAIL — cannot resolve `./util`.

- [ ] **Step 3: Write the implementation**

`sdk/src/util.ts`:

```ts
/* Path-shaping helpers, exposed as twillingate.util. They exist so a site
 * does not hand-roll the same regexes: getting the uuid pattern subtly
 * wrong, or forgetting to sort query params, are both silent failures that
 * only show up as a blown-out pages breakdown weeks later.
 */

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const ULID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const NUMERIC = /^\d+$/;
const HEX = /^[0-9a-f]{24,}$/i;

export interface MaskOptions {
  /** Also mask all-digit segments. Off by default: /2024/report is real. */
  numeric?: boolean;
  /** Also mask 24+ character hex blobs (Mongo ObjectIds and similar). */
  hex?: boolean;
}

function maskSegments(s: string, opts: MaskOptions): string {
  return s
    .split("/")
    .map((seg) => {
      if (UUID.test(seg) || ULID.test(seg)) return "[id]";
      if (opts.numeric && NUMERIC.test(seg)) return "[id]";
      if (opts.hex && HEX.test(seg)) return "[id]";
      return seg;
    })
    .join("/");
}

/**
 * Replace identifier-shaped path segments with [id]. Given an absolute URL
 * this masks the path and the hash but never the host — a hostname is not a
 * segment, and hash routes need covering in hash-routing mode. Given a bare
 * path it masks the whole input.
 */
export function maskIds(value: string, opts: MaskOptions = {}): string {
  const scheme = /^[a-z][a-z0-9+.-]*:\/\//i.exec(value);
  if (!scheme) return maskSegments(value, opts);

  // Split off authority so the host is never masked, then mask path and
  // hash independently and leave the query untouched.
  const rest = value.slice(scheme[0].length);
  const slash = rest.indexOf("/");
  if (slash < 0) return value; // scheme + host only, no path to mask
  const prefix = value.slice(0, scheme[0].length + slash);
  let tail = rest.slice(slash);

  let hash = "";
  const h = tail.indexOf("#");
  if (h >= 0) {
    hash = tail.slice(h + 1);
    tail = tail.slice(0, h);
  }
  let query = "";
  const q = tail.indexOf("?");
  if (q >= 0) {
    query = tail.slice(q);
    tail = tail.slice(0, q);
  }

  const maskedHash = hash ? "#" + maskSegments(hash, opts) : "";
  return prefix + maskSegments(tail, opts) + query + maskedHash;
}

/**
 * Append allowlisted query parameters to a path, sorted by key. Sorting is
 * load-bearing: without it ?a=1&b=2 and ?b=2&a=1 become two rows in the
 * pages breakdown for one page.
 */
export function withQuery(path: string, url: string, keys: string[]): string {
  let params: URLSearchParams;
  try {
    params = new URL(url, "http://localhost").searchParams;
  } catch {
    return path;
  }
  const picked: string[] = [];
  for (const key of [...keys].sort()) {
    const v = params.get(key);
    if (v !== null) picked.push(`${encodeURIComponent(key)}=${encodeURIComponent(v)}`);
  }
  return picked.length ? `${path}?${picked.join("&")}` : path;
}
```

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `cd sdk && npx vitest run src/util.test.ts`
Expected: PASS, 13 tests.

- [ ] **Step 5: Commit**

```bash
git add sdk/src/util.ts sdk/src/util.test.ts
git commit -m "feat(sdk): add util.maskIds and util.withQuery"
```

---

### Task 2: resolve `data-mask-url` into a mask function

**Files:**
- Create: `sdk/src/mask.ts`
- Create: `sdk/src/mask.test.ts`

**Interfaces:**
- Consumes: `maskIds` from `./util` (Task 1).
- Produces: `resolveMask(spec: MaskSpec | undefined): ((href: string) => string) | null | undefined` and `type MaskSpec = string | RegExp | ((href: string) => string)`. Returns `undefined` when no mask was requested, `null` when one was requested but could not be resolved (the fail-closed signal).

- [ ] **Step 1: Write the failing test**

`sdk/src/mask.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveMask } from "./mask";

afterEach(() => {
  vi.restoreAllMocks();
  delete (globalThis as Record<string, unknown>).myMask;
});

describe("resolveMask", () => {
  it("returns undefined when no mask is requested", () => {
    expect(resolveMask(undefined)).toBeUndefined();
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
    const f = resolveMask("/\\/account\\/\\d+/g")!;
    expect(f("https://a.example.com/account/8812/edit"))
      .toBe("https://a.example.com/[id]/edit");
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
  // into "drop pageviews". Falling back to identity would ship the raw path.
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
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd sdk && npx vitest run src/mask.test.ts`
Expected: FAIL — cannot resolve `./mask`.

- [ ] **Step 3: Write the implementation**

`sdk/src/mask.ts`:

```ts
import { maskIds, type MaskOptions } from "./util";

/**
 * What data-mask-url and init({maskUrl}) accept. The attribute can only
 * carry a string; init additionally takes the native values. A string
 * resolves through this one function either way, so the two entry points
 * cannot drift.
 */
export type MaskSpec = string | RegExp | ((href: string) => string);

/** Token names data-mask-url accepts for the built-in masker. */
const BUILTINS = new Set(["uuid", "numeric", "hex"]);

/** Parse a "/pattern/flags" literal. Returns null if it will not compile. */
function parseRegExp(spec: string): RegExp | null {
  const end = spec.lastIndexOf("/");
  if (end <= 0) return null;
  try {
    return new RegExp(spec.slice(1, end), spec.slice(end + 1));
  } catch {
    return null;
  }
}

/**
 * Resolve a mask spec to a function over the full href.
 *
 * Returns undefined when nothing was asked for, and null when something was
 * asked for and could not be provided. Null is the fail-closed signal: the
 * caller drops pageviews rather than sending unmasked ones, because a site
 * that configured masking and got a typo must not silently ship
 * /account/8812.
 */
export function resolveMask(spec: MaskSpec | undefined): ((href: string) => string) | null | undefined {
  if (spec === undefined || spec === null || spec === "") return undefined;

  if (typeof spec === "function") return spec;
  if (spec instanceof RegExp) return (href) => href.replace(spec, "[id]");

  if (spec.startsWith("/")) {
    const re = parseRegExp(spec);
    if (!re) {
      console.warn(`twillingate: data-mask-url is not a valid regexp: ${spec}`);
      return null;
    }
    return (href) => href.replace(re, "[id]");
  }

  const tokens = spec.split(",").map((t) => t.trim()).filter(Boolean);
  if (tokens.length > 0 && tokens.every((t) => BUILTINS.has(t))) {
    const opts: MaskOptions = {
      numeric: tokens.includes("numeric"),
      hex: tokens.includes("hex"),
    };
    return (href) => maskIds(href, opts);
  }

  const fn = (globalThis as Record<string, unknown>)[spec];
  if (typeof fn !== "function") {
    console.warn(`twillingate: data-mask-url names no function: ${spec}`);
    return null;
  }
  return fn as (href: string) => string;
}
```

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `cd sdk && npx vitest run src/mask.test.ts`
Expected: PASS, 9 tests.

- [ ] **Step 5: Commit**

```bash
git add sdk/src/mask.ts sdk/src/mask.test.ts
git commit -m "feat(sdk): resolve data-mask-url specs to a mask function"
```

---

### Task 3: emit `$host`/`$path`/`$utm_*` and thread listeners

**Files:**
- Modify: `sdk/src/twillingate.ts` — `InitOptions`, `PageviewInfo`, `init`, `page`
- Test: `sdk/src/twillingate.test.ts`

**Interfaces:**
- Consumes: `resolveMask`, `MaskSpec` (Task 2).
- Produces: `InitOptions.maskUrl?: MaskSpec`; `PageviewInfo` becomes `{url, host, path, referrer, attributes}`; `page()` emits `$host`, `$path`, `$utm_source`, `$utm_medium`, `$utm_campaign`, `$referrer`.

- [ ] **Step 1: Write the failing test**

Append to `sdk/src/twillingate.test.ts` (read the file first — it has helpers for a jsdom location and for capturing sent batches; reuse them rather than inventing new ones):

```ts
it("emits $host and $path instead of $url", async () => {
  // location: https://shop.example.com/pricing?utm_source=news
  const tg = newTracker({ url: "https://shop.example.com/pricing?utm_source=news" });
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$host).toBe("shop.example.com");
  expect(ev.attributes.$path).toBe("/pricing");
  expect(ev.attributes.$utm_source).toBe("news");
  expect(ev.attributes.$url).toBeUndefined();
});

it("strips the query string from $path", async () => {
  const tg = newTracker({ url: "https://a.example.com/s?tab=billing" });
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$path).toBe("/s");
});

it("applies maskUrl before the first pageview", async () => {
  const tg = newTracker({
    url: "https://a.example.com/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit",
    maskUrl: "uuid",
  });
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$path).toBe("/account/[id]/edit");
});

// UTMs are read off the original href, so a mask that eats the query
// cannot cost attribution.
it("keeps utm when the mask strips the query", async () => {
  const tg = newTracker({
    url: "https://a.example.com/x?utm_source=news",
    maskUrl: (href: string) => href.split("?")[0],
  });
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$utm_source).toBe("news");
  expect(ev.attributes.$path).toBe("/x");
});

// Two listeners rewriting $path must compose. Without threading the second
// starts from the raw path and the first rule's id leaks.
it("threads host and path through the listener chain", async () => {
  const tg = newTracker({ url: "https://a.example.com/account/88/orders/12" });
  tg.page(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
  tg.page(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$path).toBe("/account/[id]/orders/[id]");
});

it("gives listeners the post-mask url", async () => {
  const seen: string[] = [];
  const tg = newTracker({
    url: "https://a.example.com/u/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31",
    maskUrl: "uuid",
  });
  tg.page(({ url }) => {
    seen.push(url);
  });
  tg.page();
  await firstEvent(tg);
  expect(seen[0]).toBe("https://a.example.com/u/[id]");
});

// Dedup keys on the raw path: two different accounts are two pageviews
// even though both report as /account/[id].
it("does not dedup distinct raw paths that mask alike", async () => {
  const tg = newTracker({ url: "https://a.example.com/account/1", maskUrl: "uuid,numeric" });
  tg.page();
  setLocation(tg, "https://a.example.com/account/2");
  tg.page();
  expect(await eventCount(tg)).toBe(2);
});

// Fail closed: a mask that throws drops the pageview rather than sending
// the raw path it was there to hide.
it("drops pageviews when the mask throws", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const tg = newTracker({
    url: "https://a.example.com/account/8812",
    maskUrl: () => {
      throw new Error("boom");
    },
  });
  tg.page();
  expect(await eventCount(tg)).toBe(0);
  expect(warn).toHaveBeenCalled();
});

it("drops pageviews when data-mask-url names no function", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const tg = newTracker({ url: "https://a.example.com/x", maskUrl: "noSuchFunction" });
  tg.page();
  expect(await eventCount(tg)).toBe(0);
  expect(warn).toHaveBeenCalled();
});
```

If `newTracker`, `firstEvent`, `eventCount` or `setLocation` do not exist in the test file, write them once at the top of the file as local helpers over the existing patterns — do not duplicate setup into each test.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd sdk && npx vitest run src/twillingate.test.ts`
Expected: FAIL — `$host` undefined, `maskUrl` not an `InitOptions` field.

- [ ] **Step 3: Extend `InitOptions` and `PageviewInfo`**

`sdk/src/twillingate.ts`. Add the import:

```ts
import { resolveMask, type MaskSpec } from "./mask";
```

In `InitOptions`, after `autoPageviews`:

```ts
  /**
   * Rewrite the URL before it is split into $host and $path. Accepts the
   * same strings as data-mask-url ("uuid", "uuid,numeric", "/re/flags", a
   * global function name) plus a RegExp or a function directly.
   *
   * A mask that cannot be resolved, throws, or returns a non-string drops
   * pageviews: shipping the raw path would defeat the point.
   */
  maskUrl?: MaskSpec;
  /**
   * "history" (default) or "hash". In hash mode $path is pathname + hash
   * and hashchange fires a pageview; in history mode a hash change is an
   * in-page anchor and is ignored.
   */
  routing?: "history" | "hash";
```

Replace `PageviewInfo`:

```ts
/**
 * What a page listener sees. host and path are threaded: each listener
 * receives the previous listener's output, so rules split across several
 * page() calls compose instead of clobbering each other. url is the
 * post-mask URL — handing over the raw href would let a mask scrub a
 * parameter and then leak it straight back.
 */
export interface PageviewInfo {
  url: string;
  host: string;
  path: string;
  referrer: string;
  attributes: Record<string, unknown>;
}
```

- [ ] **Step 4: Store the resolved mask in `init`**

Add two fields to the class alongside `lastPage`:

```ts
  private mask: ((href: string) => string) | null | undefined;
  private routing: "history" | "hash" = "history";
```

In `init`, before the `autoPageviews` block:

```ts
    this.mask = resolveMask(opts.maskUrl);
    this.routing = opts.routing === "hash" ? "hash" : "history";
```

`resolveMask` already warned if it returned `null`; do not warn again.

- [ ] **Step 5: Rewrite `page()`**

Replace the body after the listener-registration branch. The order is load-bearing: UTMs off the original href, then mask, then split, then listeners.

```ts
    if (!this.ok()) return;

    let href: string;
    let rawKey: string; // dedup key — always the raw location, never masked
    if (typeof arg === "string" && arg !== "") {
      try {
        href = new URL(arg, location.href).href;
      } catch {
        href = arg;
      }
      rawKey = arg;
    } else {
      if (arg && typeof arg === "object") attrs = { ...arg, ...attrs };
      href = location.href;
      rawKey = location.pathname + location.search +
        (this.routing === "hash" ? location.hash : "");
    }
    if (rawKey === this.lastPage) return;
    this.lastPage = rawKey;

    // UTM comes off the ORIGINAL href: a mask that strips the query must
    // not cost campaign attribution.
    const utm = utmFrom(href);

    if (this.mask === null) {
      // A mask was configured and could not be resolved. Fail closed.
      return;
    }
    let masked = href;
    if (this.mask) {
      try {
        masked = this.mask(href);
      } catch (e) {
        console.warn("twillingate: mask threw, dropping pageview", e);
        return;
      }
      if (typeof masked !== "string") {
        console.warn("twillingate: mask returned a non-string, dropping pageview");
        return;
      }
    }

    const split = splitLocation(masked, this.routing);
    if (!split) {
      console.warn(`twillingate: mask produced an unusable url, dropping pageview: ${masked}`);
      return;
    }
    let { host, path } = split;

    let attributes: Record<string, unknown> = {
      $host: host, $path: path, $referrer: document.referrer, ...utm, ...attrs,
    };
    for (const listener of this.pageListeners) {
      const r = listener({ url: masked, host, path, referrer: document.referrer, attributes });
      if (r === false) return;
      if (r && typeof r === "object") {
        attributes = { ...attributes, ...r };
        // Thread: the next listener sees this one's output.
        if (typeof r.$host === "string") host = r.$host;
        if (typeof r.$path === "string") path = r.$path;
      }
    }
    if (typeof attributes.$path !== "string" || attributes.$path === "") {
      console.warn("twillingate: a listener produced no $path, dropping pageview");
      return;
    }
    this.emit("$pageview", attributes);
```

- [ ] **Step 6: Add the two module-level helpers**

Near the other free functions at the bottom of `twillingate.ts`:

```ts
/** Campaign parameters, read from the original href before any masking. */
function utmFrom(href: string): Record<string, string> {
  const out: Record<string, string> = {};
  let params: URLSearchParams;
  try {
    params = new URL(href, "http://localhost").searchParams;
  } catch {
    return out;
  }
  for (const [param, key] of [
    ["utm_source", "$utm_source"],
    ["utm_medium", "$utm_medium"],
    ["utm_campaign", "$utm_campaign"],
  ] as const) {
    const v = params.get(param);
    if (v) out[key] = v;
  }
  return out;
}

/**
 * Split a URL into the host and path that get stored. The query is always
 * dropped; the hash is kept only in hash-routing mode, where it IS the
 * route. The "#" is retained so the client route /app/#/settings stays
 * distinguishable from the server route /app/settings.
 */
function splitLocation(
  href: string,
  routing: "history" | "hash",
): { host: string; path: string } | null {
  let u: URL;
  try {
    u = new URL(href, location.href);
  } catch {
    return null;
  }
  let path = u.pathname || "/";
  if (routing === "hash" && u.hash) {
    // Strip a hash-internal query: $path carries no query by default in
    // either mode.
    const route = u.hash.slice(1).split("?")[0];
    path += "#" + route;
  }
  return { host: u.hostname, path };
}
```

- [ ] **Step 7: Run the tests to make sure they pass**

Run: `cd sdk && npx vitest run && npx tsc --noEmit`
Expected: PASS. Existing tests asserting `$url` will fail — update them to assert `$host`/`$path`. That is the intended breaking change, not a regression to paper over.

- [ ] **Step 8: Rebuild the bundle and commit**

```bash
cd sdk && npm run build && cd ..
git add sdk/ internal/server/twillingate.js
git commit -m "feat(sdk): send \$host and \$path with a maskUrl hook"
```

---

### Task 4: hash routing

**Files:**
- Modify: `sdk/src/twillingate.ts` — `hookHistory`
- Test: `sdk/src/twillingate.test.ts`

**Interfaces:**
- Consumes: `this.routing` (Task 3).
- Produces: `hashchange` fires a pageview in hash mode only.

- [ ] **Step 1: Write the failing test**

```ts
// Hash routing was broken outright: lastPage was pathname+search, which
// never changes in a hash SPA, so every route after the first was deduped
// away.
it("records consecutive hash routes", async () => {
  const tg = newTracker({ url: "https://a.example.com/app/#/one", routing: "hash" });
  tg.page();
  setLocation(tg, "https://a.example.com/app/#/two");
  window.dispatchEvent(new HashChangeEvent("hashchange"));
  expect(await eventCount(tg)).toBe(2);
});

it("puts pathname and hash in $path, without the hash query", async () => {
  const tg = newTracker({
    url: "https://a.example.com/app/?utm_source=news#/account/1?tab=billing",
    routing: "hash",
  });
  tg.page();
  const ev = await firstEvent(tg);
  expect(ev.attributes.$path).toBe("/app/#/account/1");
  expect(ev.attributes.$utm_source).toBe("news");
});

// In history mode #pricing is an in-page anchor, not a route. Firing a
// pageview per anchor click would flood the pages breakdown.
it("ignores hashchange in history mode", async () => {
  const tg = newTracker({ url: "https://a.example.com/x" });
  tg.page();
  setLocation(tg, "https://a.example.com/x#pricing");
  window.dispatchEvent(new HashChangeEvent("hashchange"));
  expect(await eventCount(tg)).toBe(1);
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd sdk && npx vitest run src/twillingate.test.ts -t hash`
Expected: FAIL — only one event recorded; there is no `hashchange` listener.

- [ ] **Step 3: Hook `hashchange` in hash mode**

In `hookHistory`, after the `popstate` listener:

```ts
    // Only in hash mode. In history mode a hash change is an in-page
    // anchor jump (#pricing), and treating those as pageviews would flood
    // the pages breakdown with duplicates of one route.
    if (this.routing === "hash") {
      addEventListener("hashchange", () => this.page());
    }
```

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `cd sdk && npx vitest run && npx tsc --noEmit`
Expected: PASS.

- [ ] **Step 5: Rebuild and commit**

```bash
cd sdk && npm run build && cd ..
git add sdk/ internal/server/twillingate.js
git commit -m "feat(sdk): track hash-routed SPAs with data-routing=hash"
```

---

### Task 5: `data-*` parity, `twillingate.util`, deferred first pageview

**Files:**
- Modify: `sdk/src/twillingate.ts` — `autoInit`, class export surface
- Modify: `sdk/src/entry.ts`
- Test: `sdk/src/twillingate.test.ts`

**Interfaces:**
- Consumes: everything above.
- Produces: `data-mask-url`, `data-routing`; `twillingate.util.maskIds`, `twillingate.util.withQuery`; the first auto-pageview scheduled on `setTimeout(0)`.

- [ ] **Step 1: Write the failing test**

```ts
// Every data-* attribute must have an init() equivalent. A new attribute
// added without an option would silently do nothing in bundled apps.
it("maps every data attribute to an InitOptions field", () => {
  const src = readFileSync(new URL("./twillingate.ts", import.meta.url), "utf8");
  const attrs = [...src.matchAll(/getAttribute\("data-([a-z-]+)"\)/g)].map((m) => m[1]);
  const optionFor: Record<string, string> = {
    key: "key", identity: "identity", user: "user", group: "group",
    auto: "autoPageviews", "mask-url": "maskUrl", routing: "routing",
  };
  for (const a of attrs) {
    expect(optionFor[a], `data-${a} has no InitOptions field`).toBeDefined();
    expect(src).toContain(`${optionFor[a]}?:`);
  }
});

it("exposes the helpers under twillingate.util", () => {
  expect(typeof twillingate.util.maskIds).toBe("function");
  expect(typeof twillingate.util.withQuery).toBe("function");
});

// A listener registered from an inline module after the tag must still
// affect the first pageview.
it("defers the first auto pageview by a tick", async () => {
  const tg = newTracker({ url: "https://a.example.com/account/88", autoPageviews: true });
  tg.page(({ path }) => ({ $path: path.replace(/\/\d+$/, "/[id]") }));
  await new Promise((r) => setTimeout(r, 0));
  const ev = await firstEvent(tg);
  expect(ev.attributes.$path).toBe("/account/[id]");
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd sdk && npx vitest run src/twillingate.test.ts -t "data attribute"`
Expected: FAIL — `data-mask-url` is not read.

- [ ] **Step 3: Read the two new attributes**

In `autoInit`:

```ts
  tg.init({
    key,
    url: url || undefined,
    identity: script.getAttribute("data-identity") === "identified" ? "identified" : "anonymous",
    user: script.getAttribute("data-user") || undefined,
    group: script.getAttribute("data-group") || undefined,
    autoPageviews: script.getAttribute("data-auto") !== "off",
    maskUrl: script.getAttribute("data-mask-url") || undefined,
    routing: script.getAttribute("data-routing") === "hash" ? "hash" : "history",
  });
```

- [ ] **Step 4: Expose `util` and defer the first pageview**

Add to the class:

```ts
  /** Path-shaping helpers, for use inside a page() listener. */
  readonly util = { maskIds, withQuery };
```

with `import { maskIds, withQuery } from "./util";` at the top.

In `init`, replace the synchronous first pageview:

```ts
    if (opts.autoPageviews) {
      this.hookHistory();
      // Deferred one tick so a listener registered from an inline
      // <script type="module"> after the tag — which shares the deferred
      // queue and therefore runs after this — still affects the FIRST
      // pageview, the one most likely to carry an identifier.
      setTimeout(() => this.page(), 0);
    }
```

- [ ] **Step 5: Warn on a late listener**

Add a `private firstPageviewSent = false;` field, set it to `true` at the end of `page()` just before `this.emit`, and in the listener-registration branch:

```ts
    if (typeof arg === "function") {
      if (this.firstPageviewSent) {
        console.warn(
          "twillingate: page() listener registered after the first pageview; " +
            "it will not apply to that one. Register it from an inline " +
            '<script type="module"> placed after the tag.',
        );
      }
      this.pageListeners.push(arg);
      return;
    }
```

- [ ] **Step 6: Run the tests to make sure they pass**

Run: `cd sdk && npx vitest run && npx tsc --noEmit`
Expected: PASS.

- [ ] **Step 7: Rebuild and commit**

```bash
cd sdk && npm run build && cd ..
git add sdk/ internal/server/twillingate.js
git commit -m "feat(sdk): add data-mask-url, data-routing and twillingate.util"
```

---

### Task 6: add the `sdk` commit scope

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add the scope**

In the scope paragraph, add `sdk` to the list:

```
`store`, `server`, `jobs`, `config`, `mcpserver`, `manage`, `pipeline`, `geo`,
`dashboards`, `sdk`, `cmd`, `deploy`, `ci`.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add sdk to the commit scope list"
```

---

### Task 7: full gate

- [ ] **Step 1: Run everything**

Run: `cd sdk && npm test && npm run typecheck && npm run build && cd .. && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 2: Confirm the bundle is current**

Run: `git status --short internal/server/twillingate.js`
Expected: no output. A dirty bundle here means a source change shipped without `npm run build`, which fails CI's drift check.

- [ ] **Step 3: Confirm `$url` is gone from the client**

Run: `grep -n '\$url' sdk/src/*.ts internal/server/twillingate.js`
Expected: no matches.
