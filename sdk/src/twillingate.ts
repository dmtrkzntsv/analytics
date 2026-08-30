/* twillingate SDK core — web, product and app analytics against the
 * collector's POST /api/events (docs/ingest-api.md is the normative wire
 * format). Bundled as an IIFE by build.mjs and served at /js/twillingate.js.
 *
 * Two usage modes:
 *  - snippet: <script defer src=".../js/twillingate.js" data-key="ak_…">
 *    auto-inits from data attributes with automatic pageviews, a superset
 *    with automatic pageviews;
 *  - SDK-only: load the file without data-key (or bundle this module) and
 *    call twillingate.init({...}) yourself — web, product and app events
 *    entirely from code.
 *
 * The identity model matches the legacy snippet: data-identity only
 * authorizes writing to localStorage; the server salts anonymous projects
 * no matter what the client claims, so a misconfigured client fails safe.
 */

// Substituted by the collector at serve time with its build version.
import { resolveMask, type MaskSpec } from "./mask";
import { maskIds, withQuery } from "./util";

export const VERSION = "__TWILLINGATE_VERSION__";

export interface InitOptions {
  /** Ingest key (ak_…). Required. */
  key: string;
  /** Collector base URL. Defaults to the origin the script was loaded from. */
  url?: string;
  /** Mirrors the project's identity mode; gates all localStorage writes. */
  identity?: "anonymous" | "identified";
  user?: string;
  group?: string;
  /** App analytics batch context ($platform / $app_version / $install_id). */
  platform?: string;
  appVersion?: string;
  installId?: string;
  /**
   * Automatic pageviews incl. pushState/popstate. Snippet mode defaults to
   * true; explicit init() defaults to false — turning it on is a deliberate
   * choice when instrumenting a SPA through the API.
   */
  autoPageviews?: boolean;
  /**
   * Rewrite the URL before it is split into $host and $path. Accepts the
   * same strings as data-mask-url ("uuid", "uuid,numeric", "/re/flags", or
   * a global function name) plus a RegExp or a function directly.
   *
   * A mask that cannot be resolved, throws, or returns a non-string DROPS
   * pageviews. Shipping the raw path would defeat the point of masking.
   */
  maskUrl?: MaskSpec;
  /**
   * "history" (default) or "hash". In hash mode $path is pathname + hash
   * and hashchange emits a pageview; in history mode a hash change is an
   * in-page anchor and is ignored.
   */
  routing?: "history" | "hash";
  /** Milliseconds events wait in the queue before a flush. */
  flushInterval?: number;
}

/**
 * What a page listener sees for each pageview, automatic or manual.
 *
 * host and path are THREADED: each listener receives the previous
 * listener's output, so rules split across several page() calls compose
 * instead of clobbering each other. url is the post-mask URL -- handing
 * over the raw href would let a mask scrub a parameter and then leak it
 * straight back through a listener.
 */
export interface PageviewInfo {
  url: string;
  host: string;
  path: string;
  referrer: string;
  attributes: Record<string, unknown>;
}

/**
 * Registered via page(fn). Return an object to merge extra attributes into
 * the pageview, false to cancel it, anything else to just observe.
 */
export type PageListener = (page: PageviewInfo) => Record<string, unknown> | false | void;

interface Event {
  id: string;
  ts: string;
  name: string;
  attributes: Record<string, unknown>;
}

interface Batch {
  key: string;
  attributes: Record<string, unknown>;
  events: Event[];
}

// Persisted under the twillingate_* names; the analytics_* fallbacks keep
// identified-mode visitors continuous across the legacy snippet's removal.
const VISITOR = "twillingate_visitor";
const USER = "twillingate_user";
const USER_NAME = "twillingate_user_name";
const GROUP = "twillingate_group";
const GROUP_NAME = "twillingate_group_name";
const QUEUE = "twillingate_queue";
const LEGACY = { [VISITOR]: "analytics_visitor", [USER]: "analytics_user", [GROUP]: "analytics_group" };

const MAX_BATCH = 500; // server cap per docs/ingest-api.md
const FLUSH_AT = 20; // flush early once this many events queue up
const MAX_STORED_BATCHES = 50; // offline queue bound: oldest dropped first

function ls(name: string): string | null {
  try {
    return localStorage.getItem(name);
  } catch {
    return null;
  }
}

function lsSet(name: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(name);
    else localStorage.setItem(name, value);
  } catch {
    /* storage unavailable: identity simply does not persist */
  }
}

function uuid(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function ignored(): boolean {
  if (ls("twillingate_ignore") === "true" || ls("analytics_ignore") === "true") return true;
  if (/^localhost$|^127(\.\d+){3}$|^\[::1\]$/.test(location.hostname)) return true;
  if (location.protocol === "file:") return true;
  if (navigator.webdriver) return true;
  return false;
}

// One-time migration from the legacy snippet's storage. The snippet itself
// is gone, but returning visitors still arrive holding its keys.
function migrated(name: keyof typeof LEGACY): string | null {
  const v = ls(name);
  if (v !== null) return v;
  const legacy = ls(LEGACY[name]);
  if (legacy !== null) lsSet(name, legacy);
  return legacy;
}

export class Twillingate {
  private key = "";
  private url = "";
  private identified = false;
  private userId: string | null = null;
  private userName: string | null = null;
  private groupId: string | null = null;
  private groupName: string | null = null;
  private defaultAttrs: Record<string, unknown> = {};
  private pageListeners: PageListener[] = [];
  private platform: string | null = null;
  private appVersion: string | null = null;
  private installId: string | null = null;
  private flushInterval = 1000;

  private queue: Event[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private lastPage: string | null = null;
  private hooked = false;
  // undefined = no mask configured; null = one was configured and could
  // not be resolved, which drops pageviews (fail closed).
  private mask: ((href: string) => string) | null | undefined;
  private routing: "history" | "hash" = "history";
  private firstPageviewSent = false;

  /** Path-shaping helpers, for use inside a page() listener. */
  readonly util = { maskIds, withQuery };
  private ready = false;

  init(opts: InitOptions): void {
    if (!opts || !opts.key) {
      console.warn("twillingate: init requires a key");
      return;
    }
    this.key = opts.key;
    this.url = (opts.url || scriptOrigin() || "").replace(/\/$/, "");
    if (!this.url) {
      console.warn("twillingate: init requires a url when not loaded via <script>");
      return;
    }
    this.identified = opts.identity === "identified";
    this.userId = opts.user ? String(opts.user) : this.identified ? migrated(USER) : null;
    this.userName = this.identified ? ls(USER_NAME) : null;
    this.groupId = opts.group ? String(opts.group) : migrated(GROUP);
    this.groupName = ls(GROUP_NAME);
    this.platform = opts.platform || null;
    this.appVersion = opts.appVersion || null;
    this.installId = opts.installId || null;
    if (opts.flushInterval !== undefined) this.flushInterval = opts.flushInterval;
    // Resolved before any pageview can fire, so data-mask-url covers the
    // entry page -- the one most likely to carry an identifier.
    this.mask = resolveMask(opts.maskUrl);
    this.routing = opts.routing === "hash" ? "hash" : "history";
    this.ready = true;

    this.replayStored();
    if (typeof addEventListener === "function") {
      addEventListener("online", () => this.replayStored());
      // pagehide covers navigations and tab closes; visibilitychange the
      // mobile cases where pagehide never fires. Both drain via sendBeacon.
      addEventListener("pagehide", () => this.flush(true));
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "hidden") this.flush(true);
      });
    }
    if (opts.autoPageviews) {
      this.hookHistory();
      // Synchronous, deliberately. Deferring this by a tick would let a
      // listener registered from an inline module still affect it, but an
      // app that navigates during hydration would then have the entry
      // pageview fire AFTER its pushState -- reporting the wrong location
      // and deduping against it. Masking the entry page is data-mask-url's
      // job (resolved above, before this line); a listener registered too
      // late gets a warning from page() instead.
      this.page();
    }
  }

  /**
   * $pageview, deduped against the previous path. Overloads:
   *
   *   page()                  — record the current page
   *   page("/settings")       — record an explicit path
   *   page({section: "docs"}) — current page with extra attributes
   *   page(fn)                — register a PageListener called for every
   *                             pageview (automatic ones included); it can
   *                             enrich attributes or cancel the event
   */
  page(arg?: string | Record<string, unknown> | PageListener | null, attrs?: Record<string, unknown>): void {
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
    if (!this.ok()) return;

    let href: string;
    // The dedup key is always the RAW location. Keying on the masked path
    // would collapse /account/1 -> /account/2 into a single pageview.
    let rawKey: string;
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
      rawKey =
        location.pathname + location.search +
        (this.routing === "hash" ? location.hash : "");
    }
    if (rawKey === this.lastPage) return;
    this.lastPage = rawKey;

    // Campaign parameters come off the ORIGINAL href: a mask that strips
    // the query must not cost attribution.
    const utm = utmFrom(href);

    if (this.mask === null) return; // configured but unresolvable: fail closed
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
        if (typeof r.$host === "string") host = r.$host;
        if (typeof r.$path === "string") path = r.$path;
      }
    }
    if (typeof attributes.$path !== "string" || attributes.$path === "") {
      console.warn("twillingate: a listener produced no $path, dropping pageview");
      return;
    }
    this.firstPageviewSent = true;
    this.emit("$pageview", attributes);
  }

  /** App analytics $screen_view. */
  screen(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ok() || !name) return;
    this.emit("$screen_view", { $screen: String(name), ...attrs });
  }

  /** Opt-in product event. */
  track(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ok() || !name) return;
    this.emit(String(name), attrs || {});
  }

  /**
   * Default attributes merged under every event's own (event attributes
   * win). Successive calls merge; attrs(null) clears them all.
   */
  attrs(attrs: Record<string, unknown> | null): void {
    if (attrs === null) {
      this.defaultAttrs = {};
      return;
    }
    this.defaultAttrs = { ...this.defaultAttrs, ...attrs };
  }

  /**
   * Set the user ($user_id) and optional display name ($user_name).
   * Persisted for identified projects, so every later event — this page
   * and future loads — carries the identity. Events already sent stay
   * unattributed: no retroactive stitching. The name is only stored
   * server-side for identified projects; anonymous ones ignore it.
   */
  identify(user: string, name?: string): void {
    this.userId = user ? String(user) : null;
    this.userName = name ? String(name) : null;
    if (this.identified) {
      lsSet(USER, this.userId);
      lsSet(USER_NAME, this.userName);
    }
  }

  /** Set the group ($group_id) and optional display name ($group_name). */
  group(id: string, name?: string): void {
    this.groupId = id ? String(id) : null;
    this.groupName = name ? String(name) : null;
    lsSet(GROUP, this.groupId);
    lsSet(GROUP_NAME, this.groupName);
  }

  /**
   * Required on logout: without it the next person on a shared browser
   * inherits the previous user's identity.
   */
  reset(): void {
    this.userId = null;
    this.userName = null;
    this.groupId = null;
    this.groupName = null;
    lsSet(USER, null);
    lsSet(USER_NAME, null);
    lsSet(GROUP, null);
    lsSet(GROUP_NAME, null);
    lsSet(VISITOR, null);
  }

  /** Force-send everything queued. */
  flush(unloading = false): void {
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    while (this.queue.length > 0) {
      const events = this.queue.splice(0, MAX_BATCH);
      this.send({ key: this.key, attributes: this.batchAttributes(), events }, unloading);
    }
  }

  private ok(): boolean {
    if (!this.ready) {
      console.warn("twillingate: not initialised; call twillingate.init first");
      return false;
    }
    return !ignored();
  }

  private emit(name: string, attributes: Record<string, unknown>): void {
    attributes = { ...this.defaultAttrs, ...attributes };
    this.queue.push({ id: uuid(), ts: new Date().toISOString(), name, attributes });
    if (this.queue.length >= FLUSH_AT) {
      this.flush();
      return;
    }
    if (this.flushTimer === null) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null;
        this.flush();
      }, this.flushInterval);
    }
  }

  // Identity precedence: a caller-supplied id, else the stored visitor id,
  // else nothing — the server then falls back to its rotating hash. The
  // persistent visitor id is terminal-equipment storage under ePrivacy, so
  // it is only ever written for identified projects.
  private visitorId(): string | null {
    if (this.installId) return this.installId;
    if (!this.identified) return null;
    let v = migrated(VISITOR);
    if (!v) {
      v = uuid();
      lsSet(VISITOR, v);
    }
    return v;
  }

  private batchAttributes(): Record<string, unknown> {
    const a: Record<string, unknown> = {};
    if (this.userId) a.$user_id = this.userId;
    if (this.userName) a.$user_name = this.userName;
    if (this.groupId) a.$group_id = this.groupId;
    if (this.groupName) a.$group_name = this.groupName;
    const v = this.visitorId();
    if (v) a.$install_id = v;
    if (this.platform) a.$platform = this.platform;
    if (this.appVersion) a.$app_version = this.appVersion;
    return a;
  }

  private send(batch: Batch, unloading: boolean): void {
    const endpoint = this.url + "/api/events";
    const body = JSON.stringify(batch);
    // sendBeacon with a string posts text/plain: a CORS-simple request with
    // no preflight that survives page unload. It cannot set headers, which
    // is why the key travels in the body.
    if (unloading && typeof navigator.sendBeacon === "function" && navigator.sendBeacon(endpoint, body)) {
      return;
    }
    fetch(endpoint, { method: "POST", body, keepalive: true })
      .then((res) => {
        // 5xx is transient — keep the batch for replay. 4xx is permanent
        // (bad key, bad payload): dropping beats resending it forever.
        if (res.status >= 500) this.store(batch);
      })
      .catch(() => this.store(batch));
  }

  // Offline queue: failed batches persist verbatim — each with the
  // attributes it was built with — and replay on the next load or when the
  // browser comes back online. Events carry ids and client timestamps, so
  // the server dedupes replays and keeps the original times.
  private store(batch: Batch): void {
    const stored = this.storedBatches();
    stored.push(batch);
    while (stored.length > MAX_STORED_BATCHES) stored.shift();
    lsSet(QUEUE, JSON.stringify(stored));
  }

  private storedBatches(): Batch[] {
    const raw = ls(QUEUE);
    if (!raw) return [];
    try {
      const parsed = JSON.parse(raw);
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }

  private replayStored(): void {
    const stored = this.storedBatches();
    if (stored.length === 0) return;
    lsSet(QUEUE, null); // a batch that fails again re-stores itself
    for (const batch of stored) this.send(batch, false);
  }

  private hookHistory(): void {
    if (this.hooked || typeof history === "undefined") return;
    this.hooked = true;
    const pushState = history.pushState;
    // eslint-style rebind: arrow keeps `this` on the SDK instance.
    history.pushState = (...args: Parameters<History["pushState"]>) => {
      pushState.apply(history, args);
      this.page();
    };
    addEventListener("popstate", () => this.page());
    // Hash mode only. In history mode a hash change is an in-page anchor
    // jump (#pricing), and treating those as pageviews would flood the
    // pages breakdown with duplicates of one route.
    if (this.routing === "hash") {
      addEventListener("hashchange", () => this.page());
    }
  }
}

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
 * distinguishable from the server route /app/settings, and the pathname
 * prefix is kept so two hash apps mounted at different paths stay apart.
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
    // Strip a hash-internal query: $path carries none by default, in
    // either mode.
    path += "#" + u.hash.slice(1).split("?")[0];
  }
  return { host: u.hostname, path };
}

function scriptOrigin(): string | null {
  if (typeof document === "undefined") return null;
  const script = document.currentScript as HTMLScriptElement | null;
  if (!script || !script.src) return null;
  try {
    return new URL(script.src).origin;
  } catch {
    return null;
  }
}

/**
 * Snippet-mode entry: init from the loading <script>'s data attributes.
 * Without data-key the SDK stays dormant until twillingate.init is called.
 */
export function autoInit(tg: Twillingate, script: HTMLScriptElement | null): void {
  if (!script) return;
  const key = script.getAttribute("data-key");
  if (!key) return;
  let url: string | null = null;
  try {
    url = script.src ? new URL(script.src).origin : null;
  } catch {
    url = null;
  }
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
}
