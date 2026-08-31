// The v2 API surface: attrs defaults, identify/group with display
// names, and the page() overloads (current page / explicit path / listener).
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

function lastEvent(): Record<string, unknown> {
  const events = sent[sent.length - 1].body.events;
  return events[events.length - 1];
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

describe("attrs", () => {
  it("merges defaults under every event, event attrs winning", async () => {
    const t = tg();
    t.attrs({ tier: "beta", region: "eu" });
    t.track("signup", { tier: "pro" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "pro", region: "eu" });
  });

  it("applies to pageviews and screens too", async () => {
    const t = tg();
    t.attrs({ ab_test: "b" });
    t.page();
    t.screen("/home");
    t.flush();
    await drain();
    const [pv, sc] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(pv.ab_test).toBe("b");
    expect(pv.$host).toBe("example.com"); // reserved keys unaffected
    expect(sc).toEqual({ ab_test: "b", $screen: "/home" });
  });

  it("successive calls merge; attrs(null) clears", async () => {
    const t = tg();
    t.attrs({ a: 1 });
    t.attrs({ b: 2 });
    t.track("both");
    t.attrs(null);
    t.track("none");
    t.flush();
    await drain();
    const [both, none] = sent[0].body.events.map((e) => e.attributes);
    expect(both).toEqual({ a: 1, b: 2 });
    expect(none).toEqual({});
  });
});

describe("identify and group with display names", () => {
  it("identify(user, name) sends $user_id and $user_name", async () => {
    const t = tg();
    t.identify("user-123", "Ada Lovelace");
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "user-123", $user_name: "Ada Lovelace" });
  });

  it("group(id, name) sends and persists $group_id and $group_name", async () => {
    const t = tg();
    t.group("org-9", "Acme Corp");
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $group_id: "org-9", $group_name: "Acme Corp" });
    expect(localStorage.getItem("twillingate_group")).toBe("org-9");
    expect(localStorage.getItem("twillingate_group_name")).toBe("Acme Corp");
  });

  it("persists the user name only for identified projects, and restores it", async () => {
    const anon = tg();
    anon.identify("u_1", "Plain Name");
    expect(localStorage.getItem("twillingate_user_name")).toBeNull();

    const t = tg({ identity: "identified" });
    t.identify("u_1", "Ada");
    expect(localStorage.getItem("twillingate_user_name")).toBe("Ada");

    sent = [];
    const next = tg({ identity: "identified" }); // next page load
    next.track("probe");
    next.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "u_1", $user_name: "Ada" });
  });

  it("reset() clears names along with ids", async () => {
    const t = tg({ identity: "identified" });
    t.identify("u_1", "Ada");
    t.group("org-9", "Acme");
    t.reset();
    expect(localStorage.getItem("twillingate_user_name")).toBeNull();
    expect(localStorage.getItem("twillingate_group_name")).toBeNull();
    t.track("probe");
    t.flush();
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$user_name).toBeUndefined();
    expect(attrs.$group_name).toBeUndefined();
  });
});

describe("page() overloads", () => {
  it("page(path) records an explicit path", async () => {
    const t = tg();
    t.page("/settings");
    t.flush();
    await drain();
    const ev = lastEvent();
    expect(ev.name).toBe("$pageview");
    expect((ev.attributes as Record<string, unknown>).$path).toBe("/settings");
    expect((ev.attributes as Record<string, unknown>).$host).toBe("example.com");
  });

  it("dedupes on the explicit path", async () => {
    const t = tg();
    t.page("/settings");
    t.page("/settings");
    t.page("/other");
    t.flush();
    await drain();
    expect(sent[0].body.events).toHaveLength(2);
  });

  it("page(attrs) still treats an object as extra attributes for the current page", async () => {
    const t = tg();
    t.page({ section: "docs" });
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.section).toBe("docs");
    expect(attrs.$host).toBe("example.com");
  });

  it("page(listener) registers a pageview listener that can enrich attributes", async () => {
    const t = tg();
    const seen: string[] = [];
    t.page((p) => {
      seen.push(p.path);
      return { enriched: true };
    });
    t.page("/a");
    t.flush();
    await drain();
    expect(seen).toEqual(["/a"]);
    expect((lastEvent().attributes as Record<string, unknown>).enriched).toBe(true);
  });

  it("a listener returning false cancels the pageview", async () => {
    const t = tg();
    t.page((p) => (p.path === "/private" ? false : undefined));
    t.page("/private");
    t.page("/public");
    t.flush();
    await drain();
    const paths = sent[0].body.events.map((e) => (e.attributes as Record<string, string>).$path);
    expect(paths).toEqual(["/public"]);
  });

  it("listeners fire for automatic SPA pageviews too", async () => {
    const seen: string[] = [];
    const t = tg({ autoPageviews: true });
    t.page((p) => {
      seen.push(p.path);
    });
    history.pushState(null, "", "/second");
    t.flush();
    await drain();
    expect(seen).toContain("/second");
  });
});
