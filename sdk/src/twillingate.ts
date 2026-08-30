/* twillingate SDK core — web, product and app analytics against the
 * collector's POST /api/events (docs/ingest-api.md is the normative wire
 * format). Bundled as an IIFE by build.mjs and served at /js/twillingate.js.
 *
 * Two usage modes:
 *  - snippet: <script defer src=".../js/twillingate.js" data-key="ak_…">
 *    auto-inits from data attributes with automatic pageviews, a superset
 *    of the legacy /js/script.js behaviour;
 *  - SDK-only: load the file without data-key (or bundle this module) and
 *    call twillingate.init({...}) yourself — web, product and app events
 *    entirely from code.
 *
 * The identity model matches the legacy snippet: data-identity only
 * authorizes writing to localStorage; the server salts anonymous projects
 * no matter what the client claims, so a misconfigured client fails safe.
 */

export const VERSION = "1.0.0";

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
  /** Milliseconds events wait in the queue before a flush. */
  flushInterval?: number;
}

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
// identified-mode visitors continuous for sites migrating off script.js.
const VISITOR = "twillingate_visitor";
const USER = "twillingate_user";
const GROUP = "twillingate_group";
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

// One-time migration from the legacy snippet's storage: copy, never delete —
// a page may still run script.js next to this SDK during a cut-over.
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
  private groupId: string | null = null;
  private platform: string | null = null;
  private appVersion: string | null = null;
  private installId: string | null = null;
  private flushInterval = 1000;

  private queue: Event[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private lastPage: string | null = null;
  private hooked = false;
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
    this.groupId = opts.group ? String(opts.group) : migrated(GROUP);
    this.platform = opts.platform || null;
    this.appVersion = opts.appVersion || null;
    this.installId = opts.installId || null;
    if (opts.flushInterval !== undefined) this.flushInterval = opts.flushInterval;
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
      this.page();
    }
  }

  /** Manual $pageview (deduped against the previous path). */
  page(attrs?: Record<string, unknown>): void {
    if (!this.ok()) return;
    const current = location.pathname + location.search;
    if (current === this.lastPage) return;
    this.lastPage = current;
    this.emit("$pageview", { $url: location.href, $referrer: document.referrer, ...attrs });
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
   * Persisted, so every later event — this page and future loads — carries
   * the identity. Events already sent stay unattributed: no retroactive
   * stitching.
   */
  identify(user: string, group?: string): void {
    this.userId = user ? String(user) : null;
    if (group) this.groupId = String(group);
    if (this.identified) lsSet(USER, this.userId);
    lsSet(GROUP, this.groupId);
  }

  group(id: string): void {
    this.groupId = id ? String(id) : null;
    lsSet(GROUP, this.groupId);
  }

  /**
   * Required on logout: without it the next person on a shared browser
   * inherits the previous user's identity.
   */
  reset(): void {
    this.userId = null;
    this.groupId = null;
    lsSet(USER, null);
    lsSet(GROUP, null);
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
    if (this.groupId) a.$group_id = this.groupId;
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
  }
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
  });
}
