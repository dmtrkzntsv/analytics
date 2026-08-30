# twillingate

The single normative document. Everything needed to install, configure,
instrument, connect, query and operate a twillingate deployment lives here;
the MCP endpoint serves this file verbatim as `docs://twillingate`.

Read it in order and you end up with a running, backed-up, instrumented
collector and a connected client. Skip to a heading if you already have one.

- [What twillingate is](#what-twillingate-is)
- [Install](#install)
- [Configure](#configure)
- [Instrument a website](#instrument-a-website)
- [Instrument anything else](#instrument-anything-else)
- [The event model](#the-event-model)
- [The wire format](#the-wire-format)
- [Connect an MCP client](#connect-an-mcp-client)
- [Query the data](#query-the-data)
- [Dashboards](#dashboards)
- [Operate and recover](#operate-and-recover)

---

## What twillingate is

One Go binary and one SQLite file. It collects three kinds of analytics
through a single endpoint — web pageviews, native app screen views, and
custom product events — rolls them up nightly, and exposes the result three
ways: Evidence dashboards, a read-only SQL surface, and an MCP endpoint an
AI agent can query in plain language.

It is cookieless by default. An `anonymous` project never writes to a
visitor's device and salts every identifier with a key that rotates at
midnight, so nothing links across days. Raw IP addresses and User-Agent
strings are never stored — they are enriched into a country and a browser
name at ingest and discarded.

The pieces:

| Command | Does |
| --- | --- |
| `twillingate serve -api` | Ingestion: `POST /api/events`, the SDK at `/js/twillingate.js`, `/healthz` |
| `twillingate serve -mcp` | The MCP endpoint at `/mcp` |
| `twillingate serve` | Both, on one listener unless `MCP_ADDR` says otherwise |
| `twillingate dashboards` | Renders the Evidence site from the database |
| `twillingate project`, `key`, `config` | Registry management |
| `twillingate migrate` | Applies schema migrations and exits |

Bare `serve` starts MCP only once `MCP_AUTH_DSN` is set; until then it logs
`MCP endpoint disabled` and serves ingestion alone.

---

## Install

### systemd on a VPS

```bash
# From a published release (no checkout needed):
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash

# Or from a checkout:
git clone <repo> && cd twillingate
make build
sudo ./deploy/systemd/install.sh          # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version vYY.MMDD.{build}` to pin) and verifies
its SHA256 before installing. CI publishes a release on every push to `main`.

The installer creates a system account, installs the binary to
`/usr/local/bin/twillingate`, creates `/var/lib/twillingate` (0750, owned by
the service account), installs an example `twillingate.env` loaded by both
units via `EnvironmentFile=`, renders the systemd units with the chosen user,
and enables them. It never overwrites an existing `twillingate.env`, so
re-running it to deploy a new binary is safe.

Then edit the file it flagged and create your first project — projects live
in the database, not in a shipped file:

```bash
sudo vi /etc/twillingate/twillingate.env
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -alias myapp'
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project myapp -label web'
sudo systemctl start twillingate
curl -s localhost:8080/healthz            # → {"status":"ok"}
```

Put TLS in front of `127.0.0.1:8080` — Caddy, nginx, or a Cloudflare tunnel.
The service binds to loopback by default, deliberately.

### docker compose

Tracking (`docker-compose.yml` — ingestion, the SDK, and `/mcp` once
`MCP_AUTH_DSN` is set) and reporting (`docker-compose.evidence.yml`) as one
project sharing one database:

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml > .env
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
```

`COMPOSE_FILE` is what lets every later `docker compose` command see both
files; without it, pass `-f` twice each time. Port 3000 answers `503` until
Evidence finishes its first build — roughly a minute.

### Verifying ingestion

```bash
# Expect 202 and {"accepted":1,...}
curl -i -X POST http://localhost:8080/api/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://myapp.com' \
  -d '{"key":"ak_…","events":[{"name":"$pageview",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'

# Expect 403 — the origin is not in allowed_origins
curl -i -X POST http://localhost:8080/api/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://not-allowed.com' \
  -d '{"key":"ak_…","events":[{"name":"$pageview",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'
```

---

## Configure

Infra settings are environment variables. Both systemd units load them from
`/etc/twillingate/twillingate.env`; docker compose reads a `.env` next to the
compose file; the binary itself only reads the real environment.
`.env.example` at the repo root ships every variable with its default.

### Environment variables

| Variable | Meaning |
| --- | --- |
| `LISTEN_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `PUBLIC_URL` | The collector's public base URL (`https://twillingate.example.com`). Embed snippets, MCP integration guidance and the default MCP resource URL are built from it; unset, they carry a placeholder. |
| `DATABASE_DSN` | Store DSN. Only `sqlite://<path>` today. Required. |
| `GEO_DSN` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `RETENTION_WEB_RAW_DAYS` | Days raw hits are kept before rollup. Default 7. |
| `RETENTION_WEB_AGGREGATE_DAYS` | Days aggregates are kept. Default 365. |
| `RETENTION_PRODUCT_RAW_DAYS` | Days raw product events are kept before rollup. Default 30. |
| `RETENTION_PRODUCT_AGGREGATE_DAYS` | Days product aggregates are kept. Default 365. |
| `PRODUCT_ATTRIBUTES_TOP_N` | Distinct attribute values kept per (project, day, event, key) before the rest collapse into `(other)`. Default 50. |
| `RETENTION_APP_RAW_DAYS` | Days raw app events are kept before rollup. Default 30. |
| `RETENTION_APP_AGGREGATE_DAYS` | Days app aggregates are kept. Default 365. |
| `DASHBOARDS_DB_PATH` | Database `dashboards` renders. Defaults to the `DATABASE_DSN` path. |
| `DASHBOARDS_ADDR` | Address the dashboards bind. Default `0.0.0.0:3000`. |
| `DASHBOARDS_INTERVAL` | Minimum spacing between Evidence rebuilds. Default `15m`. |
| `DASHBOARDS_PROJECT_DIR` | Evidence project in the image. Default `/opt/evidence`. |
| `DASHBOARDS_WORK_DIR` | Where the database snapshot is written. Default `/var/lib/dashboards`. |
| `MCP_AUTH_DSN` | MCP authentication: `token://<token>`, `cloudflare://<team>?aud=<tag>` or `oauth://<issuer-host>`. Unset, bare `serve` skips MCP with a warning. |
| `MCP_ADDR` | Give the MCP endpoint its own listener. Defaults to `LISTEN_ADDR` (shared). |
| `MCP_DB_PATH` | Database MCP reads for queries. Defaults to the `DATABASE_DSN` path. |
| `MCP_QUERY_TIMEOUT` | Per-query guard on the MCP `query` tool. Default `10s`. |
| `MCP_QUERY_MAX_ROWS` | Row cap on the MCP `query` tool. Default 1000. |

Litestream credentials (`LITESTREAM_ACCESS_KEY_ID`,
`LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_ENDPOINT`) live in the same
`twillingate.env`, so secrets never sit in a JSON file. Nothing in the
collector reads them.

### Projects

Projects live in the database — a registry table, not a file — and are
managed through the CLI or the MCP management tools:

```bash
twillingate project create -alias myapp -name "My App" -identity anonymous \
  -origin https://myapp.com -attr plan -attr tier
twillingate project list
twillingate project update -alias myapp -origin https://myapp.com -origin https://www.myapp.com
twillingate project rename -alias oldname -to newname   # data and ingest keys follow
twillingate project archive -alias myapp                # reversible: `project restore`
twillingate key issue -project myapp -label web
twillingate key list -project myapp
twillingate key disable -project myapp -label ios-2025
```

`project update` (and the `update_project` MCP tool) merge rather than
replace: a field you omit keeps its current value. `-origin` is the
exception — supplying it at all replaces the whole origins list. Neither can
clear origins to an empty list (empty is treated as "not supplied"); to do
that, edit the origins to `[]` in a `config export` dump and `config import`
it back.

| Key | Meaning |
| --- | --- |
| `alias` | Internal key: the `project` column on every stored row and the dashboard label. Never transmitted. New aliases must match `^[a-z0-9]+$` — `blog`, `blog2`, `2048` are fine; `my_app` and `shop-uk` are not. Immutable once created; change one with `twillingate project rename`, which rewrites the `project` column across every table in one transaction. Ingest keys follow the rename, so deployed clients keep working. An alias created before this rule keeps working. |
| `name` | Display name. |
| `identity` | `anonymous` (default) or `identified`. |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required. |
| `allowed_origins` | Origins allowed to post for this project. `*` is a wildcard — `https://*.example.com` covers every subdomain, a bare `*` allows any origin. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `retention` | Per-project override of any retention window. |
| `attributes` | Custom product-event attribute keys to break down. |

`retention` is not a CLI flag — set it through `twillingate config export`
(dumps every project as JSON) and `twillingate config import FILE` (upserts
from that JSON, or from a pre-upgrade `projects.json`, detecting the legacy
bare-array format automatically). Import never archives or deletes anything
absent from the file, so a partial edit is safe. Overrides are field-level:

```json
"retention": { "web": { "raw_days": 90 } }
```

### Attribute breakdowns

A project declares which product-event attribute keys are worth reporting on:

```bash
twillingate project update -alias myapp -attr plan -attr tier
```

`-attr` is repeatable and, like `-origin`, replaces the whole list when
supplied. Declaring a key drives two things: its own `attr_*` column in
`v_events_flat`, and a value breakdown (counts and unique users per distinct
value, per event, per day) in `agg_product_attrs` / `v_product_attrs`.

Everything sent is still stored regardless. An undeclared key has no
dedicated column but stays reachable via `json_extract(attributes,
'$.junk')`; declaring a key later does not backfill. Rollups run
unconditionally — a project with no `attributes` still gets daily counts and
totals, it just has nothing to break down.

Declaring a key bounds columns, not the values inside one. An
unbounded-cardinality key like a URL or session id would make the aggregate
grow as fast as the raw data it summarises, defeating retention.
`PRODUCT_ATTRIBUTES_TOP_N` (default 50) guards that globally: only the top N
values per key are kept and the rest collapse into one `(other)` row whose
unique-user count is recomputed from raw rather than summed. A client
sending the literal string `(other)` collides with that bucket and loses its
own count — avoid that value.

`$platform` and `$app_version` roll up automatically without being declared.
Do not add them to `attributes`: `$`-prefixed keys are reserved and never
reach the custom attribute blob, so `"attributes": ["$platform"]` extracts
nothing.

### Ingest keys

A project needs at least one key, and the key identifies the project — no
payload carries a project field. Multiple keys let a website, an iOS app and
a desktop app be retired on their own schedules.

Keys are **public by design** — they ship in app binaries and page source —
so their job is revocation and project identification, not secrecy. To
retire one: add the replacement, ship clients, watch the old label fall to
zero in the per-minute `ingest summary` log line, then disable it. Deleting
the entry is the eventual cleanup; the flag keeps the step reversible during
a botched rollout.

### Raspberry Pi and low-resource hosts

- Raise `BUFFER_FLUSH_INTERVAL` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `GEO_DSN` on `cloudflare://` or `none://`; the MaxMind provider
  downloads and holds a database in memory.

Maintenance is bounded on purpose: aggregation and pruning run once a day at
03:00 UTC, and free pages are reclaimed with incremental vacuum rather than a
full `VACUUM`, which would rewrite the whole file.

---

## Instrument a website

The collector serves its own SDK at `/js/twillingate.js`, compiled from the
TypeScript in `sdk/` and embedded in the binary. One file, two modes: a
drop-in snippet, and a full SDK for driving web, product and app analytics
from code.

### Snippet mode

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

`twillingate key issue -project <alias> -label <label>` mints the key and
prints this snippet ready to paste.

| Attribute | `init()` option | Meaning |
| --- | --- | --- |
| `data-key` | `key` | The project's ingest key. Required; public by design. |
| `data-identity` | `identity` | `anonymous` (default) or `identified`. Mirrors the project's server-side mode; the server enforces the real one regardless. `identified` additionally authorizes persisting a visitor id in localStorage — consent-relevant under ePrivacy, the same category as a cookie. |
| `data-user`, `data-group` | `user`, `group` | Set when the page is rendered already knowing who is looking at it. |
| `data-auto="off"` | `autoPageviews` | Disable automatic pageviews; drive them with `twillingate.page()`. |
| `data-mask-url` | `maskUrl` | Rewrite the URL before it is sent. See [Masking](#masking-urls). |
| `data-routing` | `routing` | `history` (default) or `hash`. See [Hash routing](#hash-routing). |

**Every `data-*` attribute has an `init()` equivalent**, enforced by a test.
The reverse does not hold: `url`, `platform`, `appVersion`, `installId` and
`flushInterval` are code-only, because an attribute can only carry a string.

Pageviews are automatic, including on `history.pushState` and `popstate`, so
single-page apps need no extra code.

### SDK-only mode

Load the file without `data-key` (it stays dormant), or bundle
`sdk/src/twillingate.ts`, and initialise yourself:

```js
twillingate.init({
  url: "https://twillingate.example.com",  // defaults to the script's origin
  key: "ak_9f3c…",
  identity: "anonymous",       // or "identified"
  autoPageviews: true,         // default false in explicit init
  maskUrl: "uuid",
  routing: "history",
  // app analytics context, sent as batch attributes:
  platform: "web",             // → $platform
  appVersion: "2.4.1",         // → $app_version
  installId: "018f…",          // → $install_id (stable per install)
  user: "u_123",               // optional page-render identity
  group: "org_9",
});
```

### Runtime API

```js
twillingate.page();                            // $pageview for the current page
twillingate.page("/settings");                 // $pageview for an explicit path
twillingate.page((p) => ({ ab: "b" }));        // register a pageview listener
twillingate.screen("/settings");               // app $screen_view
twillingate.track("signup", { plan: "pro" });  // opt-in product event
twillingate.attrs({ tier: "beta" });           // default attributes on every event
twillingate.identify("user-123", "Ada");       // $user_id + optional $user_name
twillingate.group("org-9", "Acme Corp");       // $group_id + optional $group_name
twillingate.reset();                           // on logout — see below
twillingate.flush();                           // force-send the queue
twillingate.util.maskIds(path);                // helpers, see Masking
```

- `track(name, attrs)` — product event; don't `$`-prefix your names.
- `attrs(attrs)` — default attributes merged under every event's own (event
  attributes win). Successive calls merge; `attrs(null)` clears.
- `page(arg?, attrs?)` — no argument records the current page; a string
  records that path; an object is extra attributes for the current page.
  Passing a **function** registers a pageview listener instead.
- `identify(user, name?)` — sets `$user_id` and the optional `$user_name`,
  persisted for `identified` projects so every later event carries the
  identity. Events already sent stay unattributed: there is no retroactive
  stitching. Anonymous projects ignore the name server-side.
- `group(id, name?)` — sets `$group_id`/`$group_name`; always persisted (a
  group is an organization, not a person).
- `reset()` — **required on logout.** Without it the next person on a shared
  browser inherits the previous user's identity. Clears user, group, names
  and the visitor id.
- `screen(name, attrs?)` — app analytics; pair with `platform`, `appVersion`
  and `installId` in `init` so actives, versions and screens aggregate.

### Masking URLs

A pageview carries its location **already split**: `$host` and `$path`,
stored verbatim. The server does no URL parsing, no normalization, no case
folding. That is what lets a site report `/account/[id]/edit` while the raw
`/account/8812/edit` never leaves the browser — and it keeps the pages
breakdown from growing one row per account.

`data-mask-url` receives `location.href` and returns a URL string, which the
SDK then splits:

```
https://www.shop.example.com/account/3f8a91c2-…/edit?utm_source=news#top
  → mask  → https://www.shop.example.com/account/[id]/edit
  → split → { $host: "www.shop.example.com", $path: "/account/[id]/edit" }
```

It is resolved inside `init()`, **before the first pageview**, so the entry
page — the one most likely to carry an identifier — is covered with no
timing rules. Three value forms:

| Form | Detection | Behaviour |
| --- | --- | --- |
| Built-ins | every comma-separated token is `uuid`, `numeric` or `hex` | `util.maskIds(href, opts)` |
| Regexp | value starts with `/` | parsed as `/pattern/flags`; matches replaced with `[id]` |
| Function | anything else | `window[value]`, called with the href |

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…" data-identity="anonymous"
        data-mask-url="uuid"></script>
```

The function form works because inline scripts run at parse time while the
SDK is deferred, so `window.maskPath` always exists when `init()` reads the
attribute:

```html
<script>
  function maskPath(href) { return href.replace(/\/orders\/[A-Z]{2}\d+/, "/orders/[ref]"); }
</script>
<script defer … data-mask-url="maskPath"></script>
```

`init({ maskUrl })` accepts all three plus a `RegExp` or a function
directly — values an attribute cannot carry. A string resolves through the
same code path either way, so the two entry points cannot drift.

**Campaign parameters are read from the original href before the mask
runs**, so a mask that strips or rewrites the query string cannot cost
attribution.

**Masking fails closed, loudly.** A named function that does not exist, an
unparseable regexp, a mask that throws or returns a non-string: one
`console.warn` and pageviews are **dropped** rather than sent unmasked. A
site that configured masking and typo'd an attribute must not silently ship
`/account/8812`.

#### `twillingate.util`

```js
twillingate.util.maskIds(value, opts?)      // uuid + ulid by default; { numeric, hex } opt-in
twillingate.util.withQuery(path, url, keys) // allowlisted query params, sorted
```

`maskIds` works segment by segment and is URL-aware: given an absolute URL
it masks the path **and hash** segments and never the host. Defaults replace
only shapes that are never legitimate route names — UUIDs and ULIDs. Numeric
is opt-in because `/2024/annual-report` is a real path; `hex` covers 24+
character hex blobs such as Mongo ObjectIds. Everything becomes the single
`[id]` token: what matters downstream is "this segment is an identifier".

#### Listeners

`page(fn)` registers a listener that runs for every pageview, automatic ones
included. It receives `{url, host, path, referrer, attributes}` and can
return an object to merge attributes or `false` to cancel the pageview.

**Values thread through the chain**: each listener receives the previous
one's `host` and `path`, so rules split across several calls compose instead
of clobbering each other.

```js
twillingate.page(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
twillingate.page(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
// /account/88/orders/12 → /account/[id]/orders/[id]
```

- Listeners run in registration order; each sees the previous one's output.
- `url` is the **post-mask** URL, not `location.href` — handing over the raw
  href would let a mask scrub a parameter and leak it straight back.
- Returning `false` cancels; later listeners do not run.
- A listener that produces no `$path` drops the pageview, with a warning.

The entry pageview is emitted **synchronously** during `init()`, so a
listener registered afterwards cannot affect it and gets a warning saying
so. Use `data-mask-url` for anything that must cover the entry page; it is
resolved before that first pageview. (Deferring the entry pageview by a tick
was tried and reverted: an app that navigates during hydration would then
have it fire after the `pushState`, reporting the wrong location and being
deduped away.)

### Hash routing

`data-routing="hash"` for an app whose routes live in `location.hash`:

```
https://shop.example.com/app/?utm_source=news#/account/3f8a…/edit?tab=billing
  → { $host: "shop.example.com", $path: "/app/#/account/[id]/edit", $utm_source: "news" }
```

- `$path` is `pathname + hash`, with the query stripped from both.
- `hashchange` emits a pageview **in hash mode only**. In history mode a
  hash change is an in-page anchor jump (`#pricing`), and counting those
  would flood the pages breakdown with duplicates of one route.
- Dedup keys on `pathname + search + hash`, so consecutive routes register.

The `#` is retained so the client route `/app/#/settings` stays
distinguishable from the server route `/app/settings`, and the pathname
prefix is kept so two hash apps mounted at different paths stay apart.

`util.maskIds` masks hash segments too, so `data-mask-url="uuid"` covers
hash routes with no extra configuration.

### Query-string routing

`?tab=billing` routing needs no mode: `pushState` is hooked and the dedup key
already includes `location.search`, so query-only navigations fire distinct
pageviews. `$path` carries no query string by default — appending one is an
explicit opt-in, because it can detonate cardinality:

```js
twillingate.page(({ url, path }) => ({
  $path: twillingate.util.withQuery(path, url, ["tab", "view"]),
}));
// → /settings?tab=billing
```

`withQuery` appends only allowlisted parameters and **sorts them by key**,
so `?a=1&b=2` and `?b=2&a=1` do not become two rows. Allowlist only
low-cardinality parameters — never identifiers or free text.

### Transport

Events queue briefly (~1s) and flush as one batch — on the timer, once 20
events accumulate, on `flush()`, and on page unload (`pagehide` /
`visibilitychange` via `sendBeacon`; the key travels in the JSON body
because beacons cannot set headers). Every event carries a UUID and a client
timestamp.

A batch that fails to send (network down, 5xx) persists to a bounded
localStorage queue (`twillingate_queue`, 50 batches) and replays on the next
load or when the browser fires `online`. Replays dedupe server-side by event
id and keep their original timestamps. A 4xx response (bad key, bad payload)
drops the batch instead — resending it forever helps nobody.

### Privacy behaviour

- Anonymous projects: no cookies, no localStorage writes.
- Identified projects: a visitor id persists in `twillingate_visitor`;
  identity written by the removed legacy snippet (`analytics_visitor` /
  `analytics_user` / `analytics_group`) is migrated automatically — copied,
  not moved, so returning visitors keep their identity.
- The SDK is silent on localhost, `file://` URLs and automated browsers.
- Opt a device out: `localStorage.twillingate_ignore = "true"` (the legacy
  `analytics_ignore` is honoured too).

### Helpers

The collector serves helper scripts under the same `/js/` prefix and the
same day-long cache. Load one only if its problem is yours.

| URL | What it does |
| --- | --- |
| `/js/plausible-shim.js` | Fires events from Plausible's `plausible-event-*` classes, so a site migrating off Plausible keeps its tagged CTAs without touching the markup. See `docs/plausible/`. |

`/js/script.js`, the pre-2.0 snippet, has been **removed and returns 404**.
A site still carrying that tag must swap the `src` to `/js/twillingate.js`
and rename `analytics.*` calls to `twillingate.*`. One signature changed:
`analytics.identify(user, group)` is now `twillingate.identify(user,
userName)` plus `twillingate.group(group)`. Identified-mode visitors carry
over via the storage migration above.

---

## The event model

Everything goes to one endpoint, `POST /api/events`. The event **name**
decides which family it lands in, and each family feeds a different surface:

| name | family | feeds |
| --- | --- | --- |
| `$pageview` | web | web dashboards (visitors, pages, hosts, referrers, countries, devices, UTM) and the `web_*` MCP tools |
| `$screen_view` | app | app dashboards (actives, screens, versions) and the `app_*` MCP tools |
| anything else | product | `product_events`, `product_attributes` |

The `$` prefix is reserved for the system. An unrecognized `$` **name** is
stored as an ordinary custom event with a warning (forward compatibility);
an unrecognized `$` **attribute key** is dropped, with a warning in the
response body.

### Web (`$pageview`)

Usually automatic: the SDK sends pageviews on load and on SPA navigations
(`pushState`, `popstate`, and `hashchange` in hash-routing mode). Send one
manually only from a non-browser client rendering web-like content.

A pageview carries its location already split — `$path` (**required**) and
`$host` — stored verbatim. Campaign parameters travel explicitly as
`$utm_source`, `$utm_medium` and `$utm_campaign`. `$referrer` is reduced to
a source name, and suppressed as a self-referral when its host matches
`$host`; with no `$host` there is nothing to compare against, so the
referrer is taken at face value.

`$path` may contain a `#` (hash routing) or a `?` (opt-in query routing).

A pageview is enriched from the connection that carried it: client IP for
country, User-Agent for browser and OS, `Origin` for the allowlist. **A
backend must not relay pageviews on behalf of other people** — every one
would be attributed to the backend's IP and User-Agent. The server cannot
detect this; it is a contract you keep.

Bot filtering applies to `$pageview` only, on the connection's User-Agent.
It never touches app or custom events, so an app whose HTTP library sends a
non-browser User-Agent is never dropped as a crawler.

### App (`$screen_view`)

Sent by native and desktop apps over the HTTP API. Context travels as
reserved attributes: `$install_id` (the app-install identifier — under
anonymous identity it is salted and rotated daily, more accurate than IP
hashing), `$screen`, `$session_id` (optional; without it sessions are
inferred from 30-minute gaps), `$platform`, `$app_version`, `$os_version`,
`$device_model`, `$locale`.

No User-Agent parsing ever happens for apps — they declare their own
context, which is why an Electron app is not misclassified as desktop
Chrome. `$platform` should be one of `ios`, `android`, `macos`, `windows`,
`linux`.

### Product (everything else)

Any other name is a custom product event. Don't `$`-prefix your own names.
Attributes are free-form; see [Attribute breakdowns](#attribute-breakdowns)
for which keys get their own column and value breakdown.

### Identity

Each project runs in one of two modes, set server-side:

| Mode | `$user_id`, `$install_id` | `$group_id` | `$user_name` |
| --- | --- | --- | --- |
| `anonymous` (default) | salted hash, salt rotates at 00:00 UTC | stored raw | **ignored** |
| `identified` | stored as given | stored raw | stored |

The actor an event is attributed to resolves as `$user_id` → `$install_id` →
a server-side hash of the connection. In `anonymous` mode the result is
hashed with a daily-rotating salt, so nothing links across days.

`$group_id` stays raw in both modes: it identifies an organization, not a
natural person, and hashing it would make dashboards unreadable for no real
privacy gain. If your groups are single-person, treat them as personal data.

`$user_name` is ignored entirely in `anonymous` mode. Storing a person's
name against a hash that rotates daily would defeat the anonymisation and
accumulate a fresh row per user per day.

**The server is always the enforcement point.** A client cannot opt a
project into storing raw identifiers.

---

## The wire format

Normative. Three independently written clients (iOS, Android, desktop) must
behave identically from this section alone.

### Endpoint

```
POST /api/events
```

The only ingest endpoint. There is no separate pageview, event or batch
path — a single event is a batch of one.

### Authentication

Every project has one or more **ingest keys**. The key identifies the
project, so **no payload carries a project field**.

| Client | How the key travels |
| --- | --- |
| Apps, server-side | `X-Analytics-Key: ak_…` header (preferred) |
| Browsers | `"key"` field in the JSON body |

The header wins when both are present. Browsers must use the body because
`navigator.sendBeacon` cannot set custom headers, and beacons are the only
transport that survives page unload.

An unknown key gets a plain `401` rather than a silent drop: a misconfigured
integration deserves a real error, and the response leaks nothing.

### Envelope

```jsonc
{
  "key": "ak_9f3c…",            // omit when using the header
  "attributes": {                // batch-level defaults, all optional
    "$install_id": "018f1e5a-…",
    "$user_id": "u_123",
    "$user_name": "Ada Lovelace",
    "$group_id": "org_9",
    "$group_name": "Acme Corp",
    "$session_id": "018f1e5b-…",
    "$platform": "ios",
    "$app_version": "2.4.1",
    "$os_version": "17.2",
    "$device_model": "iPhone15,2",
    "$locale": "en-US"
  },
  "events": [
    { "id": "018f1e5c-…", "ts": "2026-08-30T10:00:00Z",
      "name": "$pageview",
      "attributes": { "$host": "shop.example.com", "$path": "/account/[id]/edit",
                      "$utm_source": "newsletter",
                      "$referrer": "https://news.ycombinator.com/" } },
    { "id": "018f1e5d-…", "ts": "2026-08-30T10:00:05Z",
      "name": "$screen_view", "attributes": { "$screen": "/settings" } },
    { "id": "018f1e5e-…", "ts": "2026-08-30T10:00:09Z",
      "name": "subscribed",
      "attributes": { "plan": "pro", "$app_version": "2.5.0" } }
  ]
}
```

An event is `{id, ts, name, attributes}` and nothing else.

### Attribute merge

Batch-level `attributes` are defaults. Per-event `attributes` override them
**key by key**. That is the only merge rule, and it applies to system (`$`)
and ordinary keys alike.

The rule exists for offline queues. A queue flushed after a week may span an
app self-update: per-event override lets a client stamp `$app_version` on
only the events that differ, instead of grouping its queue by context before
flushing. It also lets a client stamp an ordinary attribute
(`"experiment": "variant_b"`) across a whole flush without repeating it.

### Reserved event names

| `name` | Stored as | Requires |
| --- | --- | --- |
| `$pageview` | web pageview | `$path` |
| `$screen_view` | app screen view | `$screen` |
| anything else | custom event | `name` |

An **unrecognized `$` name is stored as an ordinary custom event** with a
warning, never rejected. Clients update on app-store timelines while the
server updates on the operator's, so they will be out of step. Rejecting
would mean a client shipping a future `$session_start` against an older
server receives a `4xx`, which the retry rules classify as a poison batch to
drop — permanent data loss in exactly the window where forward compatibility
matters.

### Reserved attribute keys

| Group | Keys |
| --- | --- |
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$platform` `$app_version` `$os_version` `$device_model` `$locale` |
| Location | `$host` `$path` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` |
| App | `$screen` |

An **unrecognized `$` key is dropped** with a warning. It is not stored as
an ordinary attribute: keeping it would add a column to the flattened event
view for what is almost always a typo.

Location attributes are stored **verbatim**. The server does no URL parsing,
no normalization, no case folding — the client owns normalization. The
client IP and User-Agent are never stored; they are enriched into a country,
device, browser and OS name and discarded.

> **Removed in 2.0.** `$url` is no longer a reserved key. A client sending
> it gets a warning that it was ignored *and* a rejection naming `$path`.
> Send `$host` and `$path` instead, with campaign parameters as `$utm_*`.
> The `?ref=` query-parameter referrer fallback is gone with it; set
> `$referrer` explicitly.

### Timestamps and idempotency

**`ts`** is RFC 3339, UTC. Send the client's own clock reading at the moment
the event happened, not at flush time.

The server clamps `ts` to `[received − max_event_age, received + 5 minutes]`
and records the server clock separately. Out-of-range values are **clamped
and counted, never dropped**, so a device with a broken clock still
contributes. `max_event_age` equals the deployment's
`RETENTION_APP_RAW_DAYS` (30 days by default), which guarantees a clamped
event can never target a day that has already been rolled up.

**`id`** is a client-generated UUID (v7 recommended, so ids sort by time).
Supplying one is what makes at-least-once delivery safe: the write ignores a
duplicate primary key, so a batch retried after a timeout that actually
succeeded is a no-op. **Omit `id` and a replayed batch double-counts.**

### Responses and retry

```jsonc
202 {
  "accepted": 2,
  "rejected": 1,
  "errors":   [ { "index": 3, "reason": "$pageview requires $path" } ],
  "warnings": [ { "index": 1, "reason": "unknown reserved key $app_ver, ignored" } ]
}
```

`errors` and `warnings` are each capped at 10 entries. Rejection is **per
event, not per batch**: one malformed event increments `rejected` and the
rest of the batch still lands, so a single bad event cannot poison a
500-event replay.

| Code | Meaning | Client action |
| --- | --- | --- |
| 202 | Accepted for processing | Do not retry |
| 400 | Malformed envelope | Drop — poison batch |
| 401 | Unknown or disabled key | Drop |
| 403 | `Origin` present and not allowed | Drop |
| 413 | Body or event count over limit | Split and retry |
| 5xx | Server fault | Retry with backoff |

**Normative: retry only on 5xx and network failure. Any other 4xx is a
poison batch — drop it.** Retrying a 400 forever is the most common way a
hand-written client turns one bug into an outage.

The `202` is returned **before** the write; the pipeline is asynchronous. So
`accepted` counts validation, not persistence, and must not be treated as a
durability receipt.

### Limits

| Limit | Value |
| --- | --- |
| Body | 256 KB |
| Events per batch | 500 |
| Attributes per event | 50 |
| Attribute key length | 64 characters |
| Attribute value length | 512 characters (truncated, not rejected) |

### Origin and CORS

If an `Origin` header is present it must match the project's
`allowed_origins`; if absent, the request is accepted. Native apps send no
`Origin` and are unaffected.

An entry may contain `*`. A bare `*` allows any origin; elsewhere it stands
for any run of characters within the origin, so `https://*.example.com`
matches every subdomain (but not the apex, and not `http://`) and
`https://app.example.com:*` matches any port. The matched origin is echoed
back, never `*`, so a wildcard entry stays compatible with browsers that
reject a literal `*`.

Electron and Tauri renderers **do** send one, so add their scheme
(`tauri://localhost`, `app://.`, `file://`) to `allowed_origins`.

Preflight is answered with `Access-Control-Allow-Headers: Content-Type,
X-Analytics-Key` and echoes the matched origin. To skip preflight entirely,
send the key in the body with `Content-Type: text/plain`, which makes the
request CORS-simple.

### Worked offline queue

**On event**
1. Generate a UUIDv7 `id` and an RFC 3339 `ts` **now**.
2. Append `{id, ts, name, attributes}` to a durable local queue.

**On flush** (app foreground, network available, or a timer)
1. Take up to 500 events from the head of the queue.
2. POST them with the current context as batch-level `attributes`.
3. On `2xx` **or** any `4xx` other than 429: delete those events from the
   queue. A 4xx will never succeed on retry.
4. On `5xx`, 429, or a network error: keep them and retry with exponential
   backoff (for example 1s, 5s, 25s, capped at a few minutes).
5. On `413`: halve the batch size and retry.

**Never**
- Block the UI on a flush.
- Grow the queue without bound — cap it (say 10 000 events) and drop the
  oldest, which keeps recent data when a device is offline for a week.
- Retry a `400` or `401`.

---

## Connect an MCP client

`serve` exposes one MCP endpoint — streamable HTTP at
`https://twillingate.example.com/mcp`. There is no stdio server to install:
every client talks to the running collector over the network.

`twillingate serve -mcp` refuses to run unauthenticated. `MCP_AUTH_DSN` must
be set, and its scheme picks the mode:

```bash
MCP_AUTH_DSN=token://<token>
MCP_AUTH_DSN=cloudflare://<team>.cloudflareaccess.com?aud=<aud-tag>
MCP_AUTH_DSN=oauth://idp.example.com[?resource=<url>][&audience=<aud>]
```

| Mode | Pick it when | Claude Code | Claude Desktop | claude.ai |
| --- | --- | --- | --- | --- |
| `token://` | One operator, no identity provider — the simplest thing that is secure | ✅ `--header` | ⚠️ needs the `mcp-remote` bridge | ❌ cannot send custom headers |
| `cloudflare://` | Your domain is already on Cloudflare | ✅ browser login | ✅ custom connector | ✅ custom connector |
| `oauth://` | You run or rent an IdP (Keycloak, Auth0, Authentik, …) | ✅ browser login | ✅ custom connector | ✅ if the IdP does Dynamic Client Registration |

> **A connected session reads every non-archived project — including
> personal data on `identified` projects — and can use the management
> tools.** There is no per-project scoping. The credential, or the IdP
> behind it, is the whole of the access control. Treat it as an admin
> credential.

Put the MCP surface on its own hostname when you can (`MCP_ADDR` plus a
second DNS name): `/api/events` and the `/js/*` scripts must stay publicly
reachable for ingestion, and a dedicated hostname keeps the access-control
story simple.

### Enabling MCP on an installed host

The installer's unit runs bare `serve`, which starts MCP the moment its auth
is configured — until then it logs `MCP endpoint disabled` at boot. Enabling
it is one edit:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -mcp'
sudo vi /etc/twillingate/twillingate.env    # set MCP_AUTH_DSN
sudo systemctl restart twillingate
```

Set `MCP_ADDR` to give MCP its own port; unset, it shares the ingestion
listener. To run the surfaces as separate processes — independently
restartable and exposable — copy `deploy/systemd/twillingate.service` to
`twillingate-mcp.service`, change its `ExecStart=` to `twillingate serve
-mcp` (explicitly requesting `-mcp` makes missing auth a hard error), and
change the original unit's to `twillingate serve -api`.

**A `serve -mcp`-only process still runs the daily aggregation pass against
`DATABASE_DSN`** — `-mcp` only makes the HTTP listener conditional, not the
background jobs. Point a `-mcp`-only unit at a litestream replica and it
will write to that replica on every pass. Set `MCP_DB_PATH` (what MCP reads)
and `DATABASE_DSN` (what the aggregation pass writes) deliberately: either
keep `DATABASE_DSN` on a database this process is meant to own, or accept
that a two-process topology runs the idempotent daily aggregation twice.

### `token://` — a single static token

```bash
twillingate keygen -mcp        # prints: MCP_AUTH_DSN=token://ar_…
```

The token is a true secret — unlike ingest keys it reads every project and
authorizes the management tools. Rotate by minting a new one and restarting.

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The default scope is `local` — this machine, this project only. Add
`-s user` for every project you open, or `-s project` to write a checked-in
`.mcp.json`. **Do not put the token in a `-s project` config**: `.mcp.json`
is committed. Reference an environment variable instead, expanded at connect
time:

```json
{
  "mcpServers": {
    "twillingate": {
      "type": "http",
      "url": "https://twillingate.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TWILLINGATE_MCP_TOKEN}" }
    }
  }
}
```

For Claude Desktop, the connector UI cannot attach an `Authorization`
header, so a static token needs a local stdio-to-HTTP bridge and Node
installed. Edit `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "twillingate": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://twillingate.example.com/mcp",
               "--header", "Authorization: Bearer ar_…"]
    }
  }
}
```

Then quit Claude Desktop completely — closing the window leaves it in the
tray, and the config is only read at startup. Note the token sits in plain
text and `mcp-remote` is third-party code in the path of an admin
credential. If that is not acceptable, put the MCP hostname behind
Cloudflare Access and use the connector instead.

### `cloudflare://` — Access managed OAuth

Cloudflare Access acts as the OAuth authorization server: it serves the
discovery documents at the edge, runs the browser login against your chosen
identity source, and supports Dynamic Client Registration — which is what
lets claude.ai connect with zero client setup. Your only identity
infrastructure is the Cloudflare account.

1. **Route the MCP hostname through Cloudflare** — orange-cloud DNS or a
   `cloudflared` tunnel to the machine running `serve -mcp`.
2. **Create the Access application**: Zero Trust → Access controls →
   Applications → add a **self-hosted** application for the MCP hostname (or
   hostname + path). Scope it so the ingest endpoints are NOT behind it —
   `/api/events` must stay reachable without an Access session.
3. **Add an Allow policy** — your email via Google/GitHub login, a one-time
   PIN, or any configured IdP. This is your user store.
4. **Enable Managed OAuth** in the application's Advanced settings, and
   enable Dynamic Client Registration (allow-any, or list your client's
   callback in `allowed_uris`). It is opt-in for self-hosted applications.
5. **Copy the application's AUD tag** from the application overview.
6. Set `MCP_AUTH_DSN=cloudflare://yourteam.cloudflareaccess.com?aud=<tag>`.

The token a client holds under managed OAuth is opaque and validated at the
edge. What reaches the origin is the resolved identity as a JWT in the
`Cf-Access-Jwt-Assertion` header; the server validates it against the team's
public keys with the AUD tag as the audience. A request that reaches the
listener without having passed Access carries no valid assertion and is
rejected — an exposed origin port does not bypass Access. In this mode the
binary serves no discovery document and sends no challenge header; the edge
owns both.

Claude Code: add the server without a header, run `/mcp`, choose
*Authenticate*. Claude Desktop and claude.ai: Settings → Connectors → **Add
custom connector**, endpoint `https://twillingate.example.com/mcp`.

### `oauth://` — any standards-compliant IdP

The server is an OAuth 2.1 **resource server** only: it validates the JWTs
your IdP issues, and never sees a password or runs a login page.

The RFC 9728 resource URL defaults to `PUBLIC_URL` + `/mcp` (or pass
`?resource=https://…/mcp`); the expected token audience defaults to that
resource URL (`&audience=…` to override). For a local or plain-http IdP —
development only — use `oauth+insecure://`.

What the IdP must provide, verified before pointing the server at it:

1. **RFC 8414 metadata** at
   `<issuer>/.well-known/oauth-authorization-server` containing a
   `jwks_uri`. The server fetches this once at startup and refuses to boot
   if it is missing:

   ```bash
   curl -s https://auth.example.com/.well-known/oauth-authorization-server | jq .jwks_uri
   ```

   Many IdPs publish only OIDC discovery; most current Keycloak, Auth0 and
   Authentik releases serve the RFC 8414 path too, but confirm rather than
   assume.
2. **Asymmetrically signed access tokens** (RS/ES/PS). HMAC and `alg=none`
   are rejected. If your IdP issues opaque access tokens by default,
   configure it to issue JWTs for this audience.
3. **The audience claim**: tokens must carry `aud` containing the resource
   URL. In most IdPs this means registering the MCP server as an
   API/resource with that identifier and having clients request it.
4. **For claude.ai**: Dynamic Client Registration (RFC 7591), or manually
   register the client and configure its id in the connector.

Key rotation is handled: an unknown `kid` triggers a JWKS refetch, throttled
to once a minute.

### Verifying any mode

```bash
curl -si https://twillingate.example.com/mcp -X POST | head -3
# → HTTP/1.1 401; in oauth mode the WWW-Authenticate header names the
#   discovery document

curl -s https://twillingate.example.com/.well-known/oauth-protected-resource
# → oauth mode: JSON naming your issuer; cloudflare and token modes: 404

curl -s https://twillingate.example.com/healthz
# → {"status":"ok"} — health stays unauthenticated in every mode

claude mcp list          # → twillingate: … - ✓ Connected
```

Anything that speaks streamable HTTP MCP (Cursor, Zed, VS Code, custom SDK
clients) connects to the same URL.

### What you get once connected

Nineteen tools: `web_overview`, `web_breakdown`, `app_overview`,
`app_breakdown`, `product_events`, `product_attributes`, `retention`,
`identities`, a guarded read-only SQL `query`, project and ingest-key
management (`create_project`, `update_project`, `archive_project`,
`restore_project`, `issue_ingest_key`, `list_ingest_keys`,
`enable_ingest_key`, `disable_ingest_key`, `list_projects`), and
`integration_guide`, which returns paste-ready setup for a given project and
platform.

Resources: `docs://twillingate` (this document), `schema://views` (the
queryable schema) and `schema://projects` (the live registry).

Ask in plain language — "how many visitors did myapp get last week by
country", "which hosts is this project collecting from", "which screens do
people hit before they subscribe", "issue a key for the marketing site".

### Troubleshooting

**`401` on connect.** Check the endpoint directly with the curl commands
above. If curl gets a `401` too, the problem is the server or the
credential, not the client.

**Connects but no tools.** Wrong path — the endpoint is `/mcp`, not the bare
hostname.

**Desktop shows nothing after editing the config.** JSON syntax error, or
the app was never fully quit. Node also has to be on the `PATH` the GUI app
inherits, which on macOS is not your shell's; an absolute path to `npx` in
`"command"` settles it.

**Login loops in `oauth://` mode.** The IdP is probably issuing tokens
without the expected `aud`.

**Stale OAuth state.** `mcp-remote` caches under `~/.mcp-auth`; delete it to
force a fresh login.

**Token rotated but still rejected.** `claude mcp remove twillingate` and
add it again — the old header is cached in the config, not re-read from your
shell.

---

## Query the data

The `query` tool takes read-only SQL against the views below. It is
row-capped (`MCP_QUERY_MAX_ROWS`, default 1000) and time-limited
(`MCP_QUERY_TIMEOUT`, default `10s`).

**Read `schema://views` for the authoritative column list.** It is kept in
step with the migrations and carries the caveats that cannot be inferred
from the DDL. The three that matter most:

1. `day` columns are TEXT `'YYYY-MM-DD'` (UTC). Compare and `BETWEEN` as
   strings.
2. Every `v_*` view includes yesterday and today: each stitches aggregated
   history (`agg_*` tables) to a live half computed from raw rows.
3. **`v_retention` has no live half.** It refreshes at the 03:00 UTC daily
   pass; cohort days after that are ABSENT, not zero. It is populated only
   for projects with `identity=identified`, because anonymous visitor ids
   rotate daily and cohorts are undefined.

Every view carries a `project` column — always filter on it.

The web views are `v_web_daily`, `v_web_pages`, `v_web_hosts`,
`v_web_referrers`, `v_web_countries`, `v_web_devices`, `v_web_browsers`,
`v_web_os` and `v_web_utm`. App traffic has `v_app_daily`, `v_app_screens`,
`v_app_versions`, `v_app_os`, `v_app_devices` and `v_app_countries`. Product
events have `v_product_daily`, `v_product_totals` and `v_product_attrs`,
plus a per-project `v_events_flat` with one column per declared attribute.
`v_identity_daily` and `identities` join user and group activity to display
names.

Cost note: the views' live halves sessionize raw rows with window functions,
and a `WHERE` on `day` may not prune that work. Narrow ranges and the `agg_*`
tables are cheaper.

---

## Dashboards

`twillingate dashboards` renders an Evidence site from the database. In the
compose setup it is `docker-compose.evidence.yml`, serving port 3000.

It rebuilds within a minute of the database changing — it compares size and
modification time and does not need to be told. `DASHBOARDS_INTERVAL`
(default `15m`) sets the minimum spacing between rebuilds, and each rebuild
snapshots the database into `DASHBOARDS_WORK_DIR`, so the host needs room
for one more copy.

### Two servers

Ingestion on a public VPS, dashboards at home off a restored replica, so the
dashboard machine never needs to be reachable from the internet. The reader
restores the database on cron and renders it:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
docker compose -f docker-compose.evidence.yml up -d
```

Set `DASHBOARDS_DB_PATH=/data/replica.db` in `.env`: the file defaults to
`/data/twillingate.db`, which is the shared-volume case, not this one. The
compose file also carries a commented `restore` service — use it or host
cron, never both.

A failed or corrupt restore is not fatal: `restore.sh` verifies into a
temporary file and only then renames, so the previous replica keeps serving.

---

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash -s -- --yes && sudo systemctl restart twillingate` |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v26.825.1` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Export the registry | `twillingate config export > registry.json` |
| Import the registry | `twillingate config import registry.json` — also accepts a pre-upgrade `projects.json`; never archives or deletes anything absent from the file |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |

On a systemd host every CLI command needs the environment the unit loads:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'
```

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`LOG_FILE`) is optional and off by default; if enabled,
install `deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Replication with litestream

The application does not replicate anything. `serve` writes a SQLite file
and `dashboards` reads one; moving that file off the machine is a deployment
choice. You do not need any of this for a single server you are willing to
back up some other way — nothing in the collector requires object storage,
and the default compose file has no credentials in it.

Litestream streams the SQLite WAL to S3-compatible storage as it is written,
so the copy in the bucket is seconds behind rather than a day behind like a
nightly dump. That buys backup, and a read replica for the two-server
topology — both sides make outbound HTTPS connections and neither needs to
reach the other.

**Bucket and credentials.** Any S3-compatible store works; these use
Cloudflare R2.

1. Create a bucket, for example `twillingate-backup`.
2. Create an API token with **Object Read & Write** — the *writer's*
   credential.
3. For a second machine, create a second token with **Object Read** only. A
   reader that cannot write cannot corrupt the backup, however wrong its
   configuration turns out to be.

Both go in `twillingate.env`, never in a config file:

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

**Configuration.** `deploy/litestream/litestream.yml` is the whole of it:

```yaml
dbs:
  - path: /var/lib/twillingate/twillingate.db
    replicas:
      - type: s3
        bucket: ${R2_BUCKET}
        path: litestream
        endpoint: ${R2_ENDPOINT}
        sync-interval: 5s
```

> **`path:` is the identity of the replica in the bucket.** A restore asks
> for the database by the path it had on the machine that wrote it. Every
> side — writer, reader, recovery host — must use the same value byte for
> byte, even when the file lives somewhere else locally. Getting this wrong
> produces an empty restore rather than an error, which is the single most
> common way to be surprised here.

> **Writer and reader must run the same litestream major/minor version.**
> 0.5 stores backups in a new bucket format (LTX) that 0.3 cannot see, and
> vice versa — a mismatched restore reports `no matching backups found`
> rather than an error, which looks exactly like an empty bucket. The
> compose files pin `litestream/litestream:0.5`; if you pin a different
> version anywhere, pin it everywhere.

**On the writer.** For docker, uncomment the `litestream` service in
`docker-compose.yml`, copy `litestream.yml` next to the compose files and
put the four variables in `.env`. For systemd, `install.sh` installs
`litestream.service` and `/etc/litestream.yml` but not the binary — take it
from <https://litestream.io/install/>, then `sudo systemctl enable --now
litestream`.

### Backup restore drill — do this monthly

A backup you have never restored is not a backup.

```bash
# What is in the bucket? (0.5 syntax; replaces the old snapshots/generations)
litestream ltx -config /etc/litestream.yml /var/lib/twillingate/twillingate.db

litestream restore -config /etc/litestream.yml -o /tmp/check.db \
  /var/lib/twillingate/twillingate.db

# It must be a valid database, not just a file that exists:
sqlite3 /tmp/check.db 'PRAGMA quick_check;'          # expect: ok
sqlite3 /tmp/check.db 'SELECT COUNT(*) FROM projects;'
sqlite3 /tmp/check.db "SELECT MAX(day) FROM v_web_daily;"
rm /tmp/check.db
```

`deploy/litestream/restore.sh` performs the same restore-and-verify — point
`REPLICA_PATH` at a scratch file to use it as the drill.

Check the max day is recent. A restore that succeeds but is days stale means
litestream is not replicating: inspect `journalctl -u litestream`. A restore
reporting `no matching backups found` against a bucket that is being written
usually means a version mismatch.

### Disaster recovery — the host is gone

```bash
# On a fresh host:
git clone <repo> && cd twillingate && make build
sudo ./deploy/systemd/install.sh --user twillingate --yes
sudo vi /etc/twillingate/twillingate.env       # same R2 credentials

# Restore BEFORE starting the collector, so it does not create an empty db:
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'

sudo systemctl start twillingate litestream
```

> **Restore first, always.** Starting `twillingate serve` against an empty
> data directory creates a fresh database, and litestream would then
> replicate that empty database over the good backup.

Two things do not survive: the visitor salt (already rotated daily by
design, so at most one day of visitor continuity is lost) and any events
still buffered in memory when the host died — bounded by
`BUFFER_FLUSH_INTERVAL`.

### Migrating one server to two

1. Stand up the VPS, pointing litestream at the same bucket.
2. On the machine that will render dashboards, stop the `twillingate`
   service and any litestream writer.
3. Install `restore.sh` on cron with `SOURCE_DB` set to the database path
   **on the VPS** — it must match `path:` in `litestream.yml`, not wherever
   the file lands locally.
4. Drop `docker-compose.yml` from this machine and run
   `docker-compose.evidence.yml` alone, with `DASHBOARDS_DB_PATH` and the
   restore's `REPLICA_PATH` both set to `/data/replica.db`. Confirm the
   first restore lands before the next rebuild.
5. Repoint the tracking snippet's `src` at the VPS hostname.
