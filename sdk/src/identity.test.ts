// Identity lifecycle: anonymous vs identified storage semantics, migration
// from the legacy analytics_* keys, identify/group/reset, and pageviews.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate } from "./twillingate";

const URL_BASE = "https://collector.example.com";

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

async function lastAttributes(t: Twillingate): Promise<Record<string, unknown>> {
  t.track("probe");
  t.flush();
  await drain();
  return sent[sent.length - 1].body.attributes;
}

beforeEach(() => {
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/start");
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("anonymous mode", () => {
  it("never writes localStorage and sends no identity", async () => {
    const t = tg(); // identity defaults to anonymous
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBeUndefined();
    expect(attrs.$user_id).toBeUndefined();
    expect(localStorage.length).toBe(0);
  });

  it("identify() carries the user for the session but does not persist it", async () => {
    const t = tg();
    t.identify("u_42", "Ada");
    t.group("org_9");
    const attrs = await lastAttributes(t);
    expect(attrs).toMatchObject({ $user_id: "u_42", $user_name: "Ada", $group_id: "org_9" });
    expect(localStorage.getItem("twillingate_user")).toBeNull();
    expect(localStorage.getItem("twillingate_user_name")).toBeNull();
    // group is org-level, not personal: persisted in both modes
    expect(localStorage.getItem("twillingate_group")).toBe("org_9");
  });
});

describe("identified mode", () => {
  it("mints and persists a visitor id sent as $install_id", async () => {
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    const visitor = localStorage.getItem("twillingate_visitor");
    expect(visitor).toMatch(/^[0-9a-f-]{36}$/);
    expect(attrs.$install_id).toBe(visitor);

    // a second instance (next page load) reuses the same visitor
    sent = [];
    const t2 = tg({ identity: "identified" });
    const attrs2 = await lastAttributes(t2);
    expect(attrs2.$install_id).toBe(visitor);
  });

  it("an explicit installId wins over the stored visitor id", async () => {
    localStorage.setItem("twillingate_visitor", "stored-visitor");
    const t = tg({ identity: "identified", installId: "device-7" });
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBe("device-7");
  });

  it("identify() persists the user; reset() clears user, group and visitor", async () => {
    const t = tg({ identity: "identified" });
    t.identify("u_1");
    t.group("org_1");
    expect(localStorage.getItem("twillingate_user")).toBe("u_1");
    expect(localStorage.getItem("twillingate_group")).toBe("org_1");

    t.reset();
    expect(localStorage.getItem("twillingate_user")).toBeNull();
    expect(localStorage.getItem("twillingate_group")).toBeNull();
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    const attrs = await lastAttributes(t);
    expect(attrs.$user_id).toBeUndefined();
    expect(attrs.$group_id).toBeUndefined();
  });

  it("restores a persisted user on the next load", async () => {
    localStorage.setItem("twillingate_user", "u_returning");
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs.$user_id).toBe("u_returning");
  });

  it("an init-supplied user wins over the stored one", async () => {
    localStorage.setItem("twillingate_user", "u_old");
    const t = tg({ identity: "identified", user: "u_new" });
    const attrs = await lastAttributes(t);
    expect(attrs.$user_id).toBe("u_new");
  });
});

describe("migration from the legacy storage keys", () => {
  it("adopts analytics_visitor / analytics_user / analytics_group", async () => {
    localStorage.setItem("analytics_visitor", "legacy-visitor");
    localStorage.setItem("analytics_user", "legacy-user");
    localStorage.setItem("analytics_group", "legacy-group");
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBe("legacy-visitor");
    expect(attrs.$user_id).toBe("legacy-user");
    expect(attrs.$group_id).toBe("legacy-group");
    // copied, not moved: an older client may still be running elsewhere
    expect(localStorage.getItem("analytics_visitor")).toBe("legacy-visitor");
    expect(localStorage.getItem("twillingate_visitor")).toBe("legacy-visitor");
  });

  it("prefers an existing twillingate_* value over the legacy one", async () => {
    localStorage.setItem("analytics_user", "legacy");
    localStorage.setItem("twillingate_user", "current");
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs.$user_id).toBe("current");
  });
});

describe("group()", () => {
  it("sets and persists the group at runtime", async () => {
    const t = tg();
    t.group("org_77");
    const attrs = await lastAttributes(t);
    expect(attrs.$group_id).toBe("org_77");
    expect(localStorage.getItem("twillingate_group")).toBe("org_77");
  });
});

describe("pageviews", () => {
  it("page() emits $pageview with host, path and referrer", async () => {
    const t = tg();
    t.page();
    t.flush();
    await drain();
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$pageview");
    expect((ev.attributes as Record<string, unknown>).$host).toBe("example.com");
  });

  it("dedupes consecutive pageviews for the same path", async () => {
    const t = tg();
    t.page();
    t.page();
    t.flush();
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events).toHaveLength(1);
  });

  it("autoPageviews hooks pushState and popstate", async () => {
    const t = tg({ autoPageviews: true });
    history.pushState(null, "", "/second");
    history.pushState(null, "", "/third");
    t.flush();
    await drain();
    const names = sent.flatMap((s) => s.body.events.map((e) => e.name));
    expect(names.filter((n) => n === "$pageview")).toHaveLength(3); // initial + 2 pushes
    const paths = sent.flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(paths[paths.length - 1]).toBe("/third");
  });

  it("popstate back to a different path emits another pageview", async () => {
    const t = tg({ autoPageviews: true });
    history.pushState(null, "", "/a");
    history.replaceState(null, "", "/b"); // move without emitting
    window.dispatchEvent(new Event("popstate"));
    t.flush();
    await drain();
    const paths = sent.flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(paths[paths.length - 1]).toBe("/b");
  });
});

describe("location attributes", () => {
  it("emits $host and $path instead of $url", async () => {
    history.replaceState(null, "", "/pricing?utm_source=news");
    const t = tg();
    t.page();
    t.flush();
    await drain();
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("example.com");
    expect(attrs.$path).toBe("/pricing");
    expect(attrs.$utm_source).toBe("news");
    expect(attrs.$url).toBeUndefined();
  });

  it("applies a maskUrl before the pageview is sent", async () => {
    history.replaceState(null, "", "/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit");
    const t = tg({ maskUrl: "uuid" });
    t.page();
    t.flush();
    await drain();
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$path).toBe("/account/[id]/edit");
  });

  // UTM is read off the original href, so a mask that eats the query
  // cannot cost campaign attribution.
  it("keeps utm when the mask strips the query", async () => {
    history.replaceState(null, "", "/x?utm_source=news");
    const t = tg({ maskUrl: (href: string) => href.split("?")[0] });
    t.page();
    t.flush();
    await drain();
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$utm_source).toBe("news");
    expect(attrs.$path).toBe("/x");
  });

  // Without threading the second listener starts from the raw path and the
  // first rule's identifier leaks.
  it("threads host and path through the listener chain", async () => {
    history.replaceState(null, "", "/account/88/orders/12");
    const t = tg();
    t.page(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
    t.page(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
    t.page();
    t.flush();
    await drain();
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$path).toBe("/account/[id]/orders/[id]");
  });

  it("gives listeners the post-mask url", async () => {
    history.replaceState(null, "", "/u/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31");
    const seen: string[] = [];
    const t = tg({ maskUrl: "uuid" });
    t.page(({ url }) => {
      seen.push(url);
    });
    t.page();
    t.flush();
    await drain();
    expect(seen[0]).toBe("https://example.com/u/[id]");
  });

  // Dedup keys on the raw path: two accounts are two visits even though
  // both report as /account/[id].
  it("does not dedupe distinct raw paths that mask alike", async () => {
    history.replaceState(null, "", "/account/1");
    const t = tg({ maskUrl: "uuid,numeric" });
    t.page();
    history.replaceState(null, "", "/account/2");
    t.page();
    t.flush();
    await drain();
    const evs = sent.flatMap((s) => s.body.events);
    expect(evs).toHaveLength(2);
    expect((evs[0].attributes as Record<string, string>).$path).toBe("/account/[id]");
    expect((evs[1].attributes as Record<string, string>).$path).toBe("/account/[id]");
  });

  it("drops pageviews when the mask throws", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    history.replaceState(null, "", "/account/8812");
    const t = tg({
      maskUrl: () => {
        throw new Error("boom");
      },
    });
    t.page();
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn).toHaveBeenCalled();
  });

  it("drops pageviews when the mask names no function", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg({ maskUrl: "noSuchFunction" });
    t.page();
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn).toHaveBeenCalled();
  });
});

describe("hash routing", () => {
  // Hash routing was broken outright: lastPage was pathname+search, which
  // never changes in a hash SPA, so every route after the first was
  // deduped away.
  it("records consecutive hash routes", async () => {
    history.replaceState(null, "", "/app/#/one");
    const t = tg({ routing: "hash", autoPageviews: true });
    await vi.advanceTimersByTimeAsync(0);
    history.replaceState(null, "", "/app/#/two");
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    t.flush();
    await drain();
    const paths = sent.flatMap((s) =>
      s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(paths).toEqual(["/app/#/one", "/app/#/two"]);
  });

  it("puts pathname and hash in $path, without the hash query", async () => {
    history.replaceState(null, "", "/app/?utm_source=news#/account/1?tab=billing");
    const t = tg({ routing: "hash" });
    t.page();
    t.flush();
    await drain();
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$path).toBe("/app/#/account/1");
    expect(attrs.$utm_source).toBe("news");
  });

  // In history mode #pricing is an in-page anchor, not a route. A pageview
  // per anchor click would flood the pages breakdown.
  //
  // Asserts on this tracker's own batches, keyed by its ingest key: jsdom's
  // window is shared across tests and a hash-mode tracker from an earlier
  // test leaves its hashchange listener attached, so the global `sent`
  // array is not this tracker's output alone.
  it("ignores hashchange in history mode", async () => {
    history.replaceState(null, "", "/x");
    const t = tg({ key: "ak_history" , autoPageviews: true });
    await vi.advanceTimersByTimeAsync(0);
    history.replaceState(null, "", "/x#pricing");
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    t.flush();
    await drain();
    const mine = sent.filter((s) => s.body.key === "ak_history").flatMap((s) => s.body.events);
    expect(mine).toHaveLength(1);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/x");
  });
});
