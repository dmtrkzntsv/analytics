// Core SDK behaviour: init modes, payload shape, batching, transport and
// failure handling. Identity and pageview behaviour live in identity.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, autoInit } from "./twillingate";
import twillingateSource from "./twillingate.ts?raw";

const URL_BASE = "https://collector.example.com";

interface Sent {
  url: string;
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let fetchImpl: (url: string, init: { body: string }) => Promise<{ status: number }>;

function okFetch(url: string, init: { body: string }): Promise<{ status: number }> {
  sent.push({ url: String(url), body: JSON.parse(init.body) });
  return Promise.resolve({ status: 202 });
}

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  // let fetch promise callbacks (store-on-failure) settle
  await vi.waitFor(() => {});
}

beforeEach(() => {
  vi.useFakeTimers();
  sent = [];
  fetchImpl = okFetch;
  vi.stubGlobal("fetch", (url: string, init: { body: string }) => fetchImpl(url, init));
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("init", () => {
  it("warns and stays dormant without a key", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.init({ key: "" });
    t.track("x");
    expect(warn).toHaveBeenCalled();
    expect(sent).toHaveLength(0);
  });

  it("warns when no url is available outside snippet mode", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.init({ key: "ak_test" }); // no url, no loading <script>
    t.track("x");
    expect(warn.mock.calls.flat().join(" ")).toContain("url");
    expect(sent).toHaveLength(0);
  });

  it("tracks nothing before init", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    new Twillingate().track("x");
    expect(warn).toHaveBeenCalled();
    expect(sent).toHaveLength(0);
  });
});

describe("payload shape", () => {
  it("posts the documented envelope to /api/events", async () => {
    const t = tg();
    t.track("signup", { plan: "pro" });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(URL_BASE + "/api/events");
    const { body } = sent[0];
    expect(body.key).toBe("ak_test");
    expect(body.events).toHaveLength(1);
    const ev = body.events[0];
    expect(ev.name).toBe("signup");
    expect(ev.attributes).toEqual({ plan: "pro" });
    expect(String(ev.id)).toMatch(/^[0-9a-f-]{36}$/);
    expect(() => new Date(String(ev.ts)).toISOString()).not.toThrow();
  });

  it("carries app context as batch attributes", async () => {
    const t = tg({ platform: "web", appVersion: "2.4.1", installId: "018f-install" });
    t.screen("/settings");
    await drain();
    const { attributes, events } = sent[0].body;
    expect(attributes).toMatchObject({
      $platform: "web",
      $app_version: "2.4.1",
      $install_id: "018f-install",
    });
    expect(events[0].name).toBe("$screen_view");
    expect(events[0].attributes).toEqual({ $screen: "/settings" });
  });

  it("screen() merges extra attributes and requires a name", async () => {
    const t = tg();
    t.screen("", { a: 1 });
    t.screen("/home", { a: 1 });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].attributes).toEqual({ $screen: "/home", a: 1 });
  });
});

describe("batching", () => {
  it("coalesces events queued within the flush window into one POST", async () => {
    const t = tg({ flushInterval: 50 });
    t.track("a");
    t.track("b");
    t.track("c");
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events.map((e) => e.name)).toEqual(["a", "b", "c"]);
  });

  it("flushes immediately once 20 events queue up", () => {
    const t = tg({ flushInterval: 60_000 });
    for (let i = 0; i < 20; i++) t.track(`e${i}`);
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events).toHaveLength(20);
  });

  it("flush() sends the queue without waiting for the timer", () => {
    const t = tg({ flushInterval: 60_000 });
    t.track("a");
    expect(sent).toHaveLength(0);
    t.flush();
    expect(sent).toHaveLength(1);
  });

  it("uses sendBeacon when the page unloads", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon, webdriver: false });
    const t = tg({ flushInterval: 60_000 });
    t.track("bye");
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledOnce();
    const [url, body] = beacon.mock.calls[0];
    expect(url).toBe(URL_BASE + "/api/events");
    expect(JSON.parse(body).events[0].name).toBe("bye");
    expect(sent).toHaveLength(0); // beacon took it, fetch did not
  });

  it("drains on visibilitychange to hidden", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon, webdriver: false });
    const t = tg({ flushInterval: 60_000 });
    t.track("away");
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(beacon).toHaveBeenCalledOnce();
  });
});

describe("failure handling and the offline queue", () => {
  it("persists the batch when fetch rejects, and replays it on `online`", async () => {
    fetchImpl = () => Promise.reject(new TypeError("network down"));
    const t = tg();
    t.track("offline-event");
    await drain();
    expect(sent).toHaveLength(0);
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(1);
    expect(stored[0].events[0].name).toBe("offline-event");
    const originalId = stored[0].events[0].id;

    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    expect(sent).toHaveLength(1);
    // replay resends the stored batch verbatim: same id, so the server dedupes
    expect(sent[0].body.events[0].id).toBe(originalId);
    expect(localStorage.getItem("twillingate_queue")).toBeNull();
  });

  it("replays a stored queue from a previous session on init", async () => {
    localStorage.setItem(
      "twillingate_queue",
      JSON.stringify([{ key: "ak_test", attributes: {}, events: [{ id: "x", ts: "t", name: "old", attributes: {} }] }]),
    );
    tg();
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("old");
  });

  it("keeps a batch that fails on 5xx and drops one rejected with 4xx", async () => {
    fetchImpl = (url, init) => {
      sent.push({ url: String(url), body: JSON.parse(init.body) });
      return Promise.resolve({ status: 503 });
    };
    const t = tg();
    t.track("transient");
    await drain();
    expect(JSON.parse(localStorage.getItem("twillingate_queue")!)).toHaveLength(1);

    localStorage.removeItem("twillingate_queue");
    fetchImpl = (url, init) => {
      sent.push({ url: String(url), body: JSON.parse(init.body) });
      return Promise.resolve({ status: 401 });
    };
    t.track("permanent");
    await drain();
    expect(localStorage.getItem("twillingate_queue")).toBeNull();
  });

  it("bounds the offline queue at 50 batches, oldest dropped first", async () => {
    fetchImpl = () => Promise.reject(new TypeError("down"));
    const t = tg();
    for (let i = 0; i < 55; i++) {
      t.track(`e${i}`);
      t.flush();
    }
    await drain();
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(50);
    expect(stored[0].events[0].name).toBe("e5");
    expect(stored[49].events[0].name).toBe("e54");
  });

  it("survives a corrupt stored queue", async () => {
    localStorage.setItem("twillingate_queue", "{not json");
    const t = tg();
    t.track("fine");
    await drain();
    expect(sent).toHaveLength(1);
  });
});

describe("ignore rules", () => {
  it("honours twillingate_ignore and the legacy analytics_ignore", async () => {
    const t = tg();
    localStorage.setItem("twillingate_ignore", "true");
    t.track("nope");
    localStorage.removeItem("twillingate_ignore");
    localStorage.setItem("analytics_ignore", "true");
    t.track("nope2");
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("stays silent under automated browsers", async () => {
    vi.stubGlobal("navigator", { ...navigator, webdriver: true });
    const t = tg();
    t.track("robot");
    await drain();
    expect(sent).toHaveLength(0);
  });
});

describe("snippet auto-init", () => {
  function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
    const s = document.createElement("script");
    s.src = URL_BASE + "/js/twillingate.js";
    for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
    return s;
  }

  it("inits from data attributes and fires an automatic pageview", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet" }));
    t.flush();
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(URL_BASE + "/api/events");
    expect(sent[0].body.key).toBe("ak_snippet");
    expect(sent[0].body.events[0].name).toBe("$pageview");
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("example.com");
  });

  it("data-auto=off suppresses automatic pageviews", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-auto": "off" }));
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("stays dormant without data-key", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({}));
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("carries data-user and data-group into batch attributes", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-user": "u_1", "data-group": "org_9" }));
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "u_1", $group_id: "org_9" });
  });
});

describe("script tag and init parity", () => {
  // Every data-* attribute must have an InitOptions field. A new attribute
  // added without one would silently do nothing in bundled apps.
  it("maps every data attribute to an InitOptions field", () => {
    const src = twillingateSource;
    // exec loop rather than matchAll: the tsconfig lib is pinned at ES2019
    // for the shipped bundle's browser target.
    const attrs: string[] = [];
    const re = /getAttribute\("data-([a-z-]+)"\)/g;
    for (let m = re.exec(src); m !== null; m = re.exec(src)) attrs.push(m[1]);
    const optionFor: Record<string, string> = {
      key: "key", identity: "identity", user: "user", group: "group",
      auto: "autoPageviews", "mask-url": "maskUrl", routing: "routing",
    };
    expect(attrs.length).toBeGreaterThan(0);
    for (const a of attrs) {
      const opt = optionFor[a];
      expect(opt, `data-${a} has no InitOptions field`).toBeDefined();
      // Required (key: string) or optional (maskUrl?: MaskSpec) both count.
      const declared = new RegExp(`^\\s+${opt}\\??:`, "m");
      expect(declared.test(src), `InitOptions declares no ${opt}`).toBe(true);
    }
  });

  it("reads data-mask-url and data-routing", async () => {
    document.body.innerHTML = "";
    const s = document.createElement("script");
    s.src = URL_BASE + "/js/twillingate.js";
    s.setAttribute("data-key", "ak_test");
    s.setAttribute("data-mask-url", "uuid");
    s.setAttribute("data-routing", "hash");
    document.body.appendChild(s);
    history.replaceState(null, "", "/app/#/u/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31");

    const t = new Twillingate();
    autoInit(t, s);
    t.flush();
    await drain();
    const mine = sent.flatMap((x) => x.body.events);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/app/#/u/[id]");
  });

  it("exposes the helpers under twillingate.util", () => {
    const t = new Twillingate();
    expect(typeof t.util.maskIds).toBe("function");
    expect(typeof t.util.withQuery).toBe("function");
  });

  // The entry pageview is synchronous, so a listener registered after
  // init cannot affect it. That is a warning, not a silent miss -- and
  // data-mask-url, resolved during init, is the mechanism that does cover
  // the entry page.
  it("warns when a listener is registered after the first pageview", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    history.replaceState(null, "", "/account/88");
    const t = tg({ key: "ak_late", autoPageviews: true });
    t.page(({ path }) => ({ $path: path.replace(/\/\d+$/, "/[id]") }));
    t.flush();
    await drain();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("after the first pageview"));
    const mine = sent.filter((x) => x.body.key === "ak_late").flatMap((x) => x.body.events);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/account/88");
  });
});
