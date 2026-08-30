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
    t.identify("u_42", "org_9");
    const attrs = await lastAttributes(t);
    expect(attrs).toMatchObject({ $user_id: "u_42", $group_id: "org_9" });
    expect(localStorage.getItem("twillingate_user")).toBeNull();
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
    t.identify("u_1", "org_1");
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

describe("migration from the legacy script.js storage", () => {
  it("adopts analytics_visitor / analytics_user / analytics_group", async () => {
    localStorage.setItem("analytics_visitor", "legacy-visitor");
    localStorage.setItem("analytics_user", "legacy-user");
    localStorage.setItem("analytics_group", "legacy-group");
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBe("legacy-visitor");
    expect(attrs.$user_id).toBe("legacy-user");
    expect(attrs.$group_id).toBe("legacy-group");
    // copied, not moved: script.js may still run during the cut-over
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
  it("page() emits $pageview with url and referrer", async () => {
    const t = tg();
    t.page();
    t.flush();
    await drain();
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$pageview");
    expect((ev.attributes as Record<string, unknown>).$url).toContain("https://example.com");
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
    const urls = sent.flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$url));
    expect(urls[urls.length - 1]).toContain("/third");
  });

  it("popstate back to a different path emits another pageview", async () => {
    const t = tg({ autoPageviews: true });
    history.pushState(null, "", "/a");
    history.replaceState(null, "", "/b"); // move without emitting
    window.dispatchEvent(new Event("popstate"));
    t.flush();
    await drain();
    const urls = sent.flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$url));
    expect(urls[urls.length - 1]).toContain("/b");
  });
});
