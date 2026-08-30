# Splitting `$url` into `$host` and `$path` — design

Date: 2026-08-30
Status: proposed

## 1. Purpose

Today a `$pageview` carries one location field, `$url`, holding
`location.href`. The server parses it (`enrich.ParsePageURL`) into host,
path and campaign parameters, then **stores only the path**. Two
consequences:

1. **Host is discarded.** It is computed solely to suppress self-referrals
   (`handlers.go`) and thrown away. A project spanning a marketing site and
   an app collapses both `/pricing` rows into one, with no way to separate
   them.
2. **The client cannot control what is stored.** A path like
   `/account/8812/edit` is stored verbatim. The raw identifier leaves the
   browser, transits the collector, and lands in `web_hits.path` — where it
   also explodes the cardinality of the pages breakdown into one row per
   account.

This design replaces `$url` on the wire with `$host` + `$path` + explicit
`$utm_*`, makes host a stored dimension, and gives the client a masking
hook so `/account/8812/edit` can be reported as `/account/[id]/edit` —
with the raw path never leaving the device.

## 2. Non-goals

- **No server-side rewrite rules.** Normalization is a client concern.
  The server stores what it is told. There is no project-config rule
  engine, no path-cleaning UI.
- **No retroactive rewriting.** Rules apply at collection time only. Rows
  already stored are never rewritten.
- **No backfill of `host` for existing rows.** The value was never stored
  and cannot be recovered.
- **No change to the app surface.** `$screen_view` and the `app_*` tables
  are untouched.

## 3. Wire contract

### 3.1 The new shape

`$pageview` requires `$path`. The full set of location attributes:

| key | required | stored in |
| --- | --- | --- |
| `$path` | yes | `web_hits.path` |
| `$host` | no | `web_hits.host` |
| `$utm_source` | no | `web_hits.utm_source` |
| `$utm_medium` | no | `web_hits.utm_medium` |
| `$utm_campaign` | no | `web_hits.utm_campaign` |
| `$referrer` | no | input to `referrer_source` |

```json
{ "name": "$pageview",
  "attributes": {
    "$host": "shop.example.com",
    "$path": "/account/[id]/edit",
    "$utm_source": "newsletter",
    "$referrer": "https://news.ycombinator.com/" } }
```

Omitting `$host` is legal: the host breakdown shows an empty bucket for
that hit and the self-referral check is skipped (an external referrer
cannot be distinguished from an internal one, so `$referrer` is taken at
face value).

Values are stored **verbatim**. The server does no parsing, no
normalization, no case folding. `$path` may contain a `#` (hash routing,
§4.5) or a query string (§4.6) when the client chooses to send one.

### 3.2 `$url` becomes a legacy input

`internal/server/script.go` states that `script.js` is *"the frozen legacy
snippet: deployed websites load it, so it is served byte-for-byte
forever."* That snippet sends `$url` and only `$url`. The npm SDK can also
be pinned at an older version against a newer collector, and up to 50
batches may sit in `twillingate_queue` in localStorage across an upgrade.

Therefore `$url` remains **accepted but undocumented as the contract**:

- If `$path` is present, `$url` is ignored entirely.
- If `$path` is absent and `$url` is present, the server runs today's
  `enrich.ParsePageURL` and derives host, path and `utm_*` from it —
  identical to current behaviour.
- If neither is present, the event is rejected: `$pageview requires $path`.

Explicit fields always win over anything parsed from `$url`, field by
field, so a client may migrate one attribute at a time.

`docs/twillingate.md` documents `$url` under a "legacy" heading with the
above precedence, and shows it in no example.

### 3.3 Removed: the `?ref=` fallback

Today, when `CleanReferrer` yields nothing, the server falls back to the
`?ref=` query parameter parsed out of `$url`. That fallback exists only
because the server already had the query string in hand. Under the new
contract it does not, so the fallback is removed.

A site relying on `?ref=` links sets `$referrer` itself from a `page()`
listener. This is a real, if narrow, behaviour loss and is called out in
the changelog under `feat(server)!`.

## 4. Client (`twillingate.js`)

### 4.1 No new configuration API

Masking reuses the existing `page(listener)` hook. There is no
`init({ path })` option and no new top-level method. `init()` remains an
alternative to the `<script>` tag and mirrors its parameters exactly.

```js
twillingate.page(({ path }) => ({
  $path: path.replace(/^\/account\/[^/]+/, "/account/[id]"),
}));
```

### 4.2 `PageviewInfo` and listener chaining

`PageviewInfo` becomes `{ url, host, path, referrer, attributes }` — `host`
is new.

**Values thread through the chain.** Each listener receives the `host` and
`path` produced by the previous one, not the original. Without threading,
two listeners that both rewrite `$path` silently clobber each other:

```js
twillingate.page(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
twillingate.page(({ path }) => ({ $path: path.replace(/^\/orders\/\d+/, "/orders/[id]") }));
// /account/88/orders/12
//   without threading → second listener wins → /account/88/orders/[id]  (id leaks)
//   with threading    → /account/[id]/orders/[id]
```

Rules:

- Listeners run in registration order; each sees the previous one's output.
- Returning `false` cancels the pageview; later listeners do not run.
- Returning nothing observes without changing anything.
- `url` is the **post-mask** URL (§4.3), not `location.href`.

### 4.3 `data-mask-url`

The `<script>` tag is the preferred integration, and a function cannot be
written into an attribute. `data-mask-url` names one, and is resolved
inside `init()` — **before** the first pageview, so the entry page (the one
most likely to carry an identifier) is masked with no timing rules.

```html
<script defer src="https://t.example.com/js/twillingate.js"
        data-key="ak_9f3c…" data-identity="anonymous" data-mask-url="uuid"></script>
```

The mask receives `location.href` and returns a URL string, which the SDK
then splits into `$host` and `$path`.

```
https://www.shop.example.com/account/3f8a91c2-…/edit?utm_source=news#top
  → mask → https://www.shop.example.com/account/[id]/edit
  → split → { $host: "www.shop.example.com", $path: "/account/[id]/edit" }
```

Three value forms, resolved in this order:

| form | detection | behaviour |
| --- | --- | --- |
| built-ins | every comma-separated token is in `{uuid, numeric, hex}` | `util.maskIds(href, opts)` |
| regexp | value starts with `/` | parsed as `/pattern/flags`; matches replaced with `[id]` |
| function | anything else | `window[value]`, called with the href |

The function form is safe because inline scripts run at parse time while
the SDK is deferred, so the global always exists when `init()` reads the
attribute.

**UTMs are read from the original href before the mask runs**, so a mask
that strips or rewrites the query string cannot cost attribution.

**`data-mask-url` does not conflict with `page()`.** It registers as the
first listener, before any user listener can exist. Because listeners
thread (§4.2), a `page()` listener receives the already-masked value and
refines it. Order is fixed: attribute first, then `page()` listeners in
registration order.

### 4.4 Failing closed

The masking path fails closed, loudly. On any of:

- a named function that does not exist on `window`,
- an unparseable regexp,
- a mask or listener that throws,
- a mask returning a non-string or a value that will not parse as a URL,
- a listener returning a non-string `$path`,

the SDK emits one `console.warn` and **drops pageviews** rather than
sending unmasked ones. A site that configured masking and got a typo must
not silently ship `/account/8812`. Loud and empty beats quiet and leaky.

### 4.5 `data-routing` and hash routing

Hash routing is currently **broken**, not merely unsupported: `lastPage` is
`pathname + search`, which never changes in a hash SPA, so every route
after the first is deduplicated away. There is no `hashchange` listener.

`data-routing` is `"history"` (default) or `"hash"`; `init({ routing })`
mirrors it.

In hash mode:

- `$path` is `pathname + hash`, with the query stripped from both.
- The dedup key is `pathname + search + hash`.
- `hookHistory()` adds a `hashchange` listener.

```
https://shop.example.com/app/?utm_source=news#/account/3f8a…/edit?tab=billing
  → { $host: "shop.example.com", $path: "/app/#/account/[id]/edit", $utm_source: "news" }
```

The `#` is retained so the client route `/app/#/settings` stays
distinguishable from the server route `/app/settings`. The pathname prefix
is retained so two hash apps mounted at different paths stay apart.

`hashchange` fires a pageview **in hash mode only**. In history mode a hash
change is an in-page anchor jump (`#pricing`), and treating those as
pageviews would flood the pages breakdown with duplicates of one route.

The rule "`$path` carries no query string by default" holds in both modes:
a hash-internal `?tab=billing` is dropped exactly as `location.search` is.

### 4.6 Query-based routing

`?tab=billing` routing needs no mode. `pushState` is already hooked and the
dedup key already includes `location.search`, so query-only navigations
already fire distinct pageviews. The only missing piece was access to the
query, which `PageviewInfo.url` provides.

It is deliberately **opt-in via `page()`**, never automatic — appending
query parameters to `$path` can detonate cardinality.

```js
twillingate.page(({ url, path }) => ({
  $path: twillingate.util.withQuery(path, url, ["tab", "view"]),
}));
// → /settings?tab=billing
```

`withQuery` appends only allowlisted parameters and **sorts them by key**,
so `?a=1&b=2` and `?b=2&a=1` do not become two rows. That sorting is the
reason it ships as a helper rather than a docs snippet.

Docs warn: allowlist only low-cardinality parameters, never identifiers or
free text.

### 4.7 `twillingate.util`

Helpers are namespaced so the top-level API does not grow.

```js
twillingate.util.maskIds(value, opts?)      // uuid + ulid by default; { numeric, hex } opt-in
twillingate.util.withQuery(path, url, keys) // allowlisted, sorted
```

`maskIds` works segment by segment and is URL-aware: given an absolute URL
it masks the path **and hash** segments and never the host, so
`shop.example.com` is never mistaken for a segment and hash routes are
covered by `data-mask-url="uuid"` with no extra configuration. Given a bare
path it masks the whole input.

Defaults replace only shapes that are never legitimate route names — UUIDs
(any version, either case) and ULIDs. Numeric segments are **not** default:
`/2024/annual-report` is a real path. `hex` covers 24+ character hex blobs
(Mongo ObjectIds and similar). All replacements use the single `[id]`
token; what matters downstream is "this segment is an identifier", and one
token keeps aggregate rows tight.

### 4.8 First auto-pageview timing

In snippet mode `init()` currently calls `this.page()` synchronously, so a
`page()` listener registered from an inline `<script type="module">` after
the tag arrives too late for the first pageview.

The initial auto-pageview is scheduled on `setTimeout(0)` instead. Module
scripts share the deferred queue with `defer` scripts and execute in
document order, so a listener registered there lands first. If a listener
is registered after the first pageview has already gone out, the SDK
`console.warn`s.

With `data-mask-url` covering the entry page, this is a consistency fix
rather than a leak fix, but `page()` should behave the same on the first
pageview as on every later one.

### 4.9 `script.js` is untouched

The frozen legacy snippet is not modified. It keeps sending `$url` and is
served by the legacy path of §3.2 forever.

## 5. Storage

### 5.1 Migration `006_web_host.sql`

```sql
ALTER TABLE web_hits ADD COLUMN host TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_web_hosts (
    project TEXT NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, host)
) WITHOUT ROWID;

CREATE VIEW v_web_hosts AS
SELECT project, day, host, visitors, pageviews FROM agg_web_hosts
UNION ALL
SELECT project, substr(ts,1,10), host, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), host;
```

The view mirrors `v_web_pages` exactly — aggregated history stitched to a
live half computed from raw rows, so today and yesterday are included.

Rows written before the migration carry `host = ''`. The empty bucket is
documented, not hidden.

### 5.2 Fan-out

| Surface | Change |
| --- | --- |
| `store.WebHit` | `Host` field |
| `sqlite/write.go` | `host` in the `web_hits` insert |
| `sqlite/aggregate_web.go` | `{"agg_web_hosts", "host", "host", ""}` in the dimension table |
| `sqlite/registry.go` | `agg_web_hosts` in the export/import table list |
| `sqlite/retention.go` | aggregate pruning follows the same list |
| `mcpserver/tools_read.go` | `"hosts": {"v_web_hosts", "host"}` in `webDimensions`, plus the enum and tool description |
| `mcpserver/resources.go` | `v_web_hosts` in `schemaViews` |
| `evidence/sources/twillingate/` | `v_web_hosts.sql` + a tile on `pages/web/[project].md` |

## 6. Server

`resolved` gains `Host, Path, UTMSource, UTMMedium, UTMCampaign`; five new
`reservedKeys` entries map to them. The `$pageview` branch of
`handlers.go`:

1. If `$path` is set, use the explicit fields.
2. Otherwise, if `$url` is set, derive them via `ParsePageURL` (§3.2).
3. Otherwise reject.
4. `CleanReferrer(rv.Referrer, host)` using the resolved host; skip the
   self-referral check when host is empty.

`enrich.ParsePageURL` is unchanged and serves only the legacy path. Bot
filtering, identity resolution, timestamp clamping and the queue write are
unchanged.

## 7. Documentation

### 7.1 One embedded document

The MCP doc surface is four pieces today, two of which are hand-written Go
string constants in `docs_content.go` (`docsEvents`, `docsJSSDK`) bound to
source files by `docs_sync_test.go`. `docsJSSDK` is derived from
`script.js` — the frozen legacy snippet — so it already documents an API
that is not the one being changed.

`docs/twillingate.md` becomes the single normative document, embedded
through the `docs` package (the `//go:embed` pattern `docs.IngestAPI`
already uses) and exposed as `docs://twillingate`.

- It absorbs `docs/sdk.md` and `docs/ingest-api.md`, which are replaced by
  short pointers.
- `docsEvents` and `docsJSSDK` are deleted.
- `docs_sync_test.go` is repointed to bind `docs/twillingate.md` to
  `reservedKeys`: every reserved key documented, no documented key absent
  from the map. This is a stronger test than the one it replaces and
  catches the five new keys going undocumented.

Kept separate:

- **`schema://views`** — a machine reference that tracks migrations; an
  agent writing SQL should not pay for the SDK prose.
- **`integration_guide`** — injects live registry state (active key,
  identity mode, `PUBLIC_URL`) that no static document can carry.

Four resources become two, plus the unchanged tool.

### 7.2 Keeping it current

Two mechanisms, because neither alone is enough.

**Enforced.** `docs_sync_test.go` binds `docs/twillingate.md` to
`reservedKeys` (§7.1). Adding a reserved attribute without documenting it
fails `make check`. This catches the wire contract — the part most likely
to drift and most damaging when it does.

**Instructed.** `CLAUDE.md` gains a `## Documentation` section, since no
test can catch prose going stale:

> `docs/twillingate.md` is the single normative document and the only one
> the MCP endpoint serves to agents. It is not a summary of the code — it
> is the contract. Update it **in the same commit** as any change to:
>
> - reserved attribute keys or event names (`internal/server/ingest.go`)
> - the ingest wire format or its responses (`internal/server/handlers.go`)
> - the JS SDK's public API, `data-` attributes or defaults
>   (`sdk/src/twillingate.ts`)
> - queryable views or their columns (`internal/store/sqlite/migrations/`)
>
> `docs/sdk.md` and `docs/ingest-api.md` are pointers, not sources — never
> add content there. `docs_sync_test.go` enforces the reserved-key half of
> this; the rest is on you.

A `docs`-only change publishes nothing (`paths-ignore` on `docs/**`), so
same-commit updating costs no extra release and keeps the document honest
by construction.

### 7.3 Examples

Every example shows `data-identity` explicitly. The default is anonymous in
both the tag and `init()`, but a snippet that reads as "no identity
setting" invites the opposite assumption.

```html
<script defer src="https://t.example.com/js/twillingate.js"
        data-key="ak_9f3c…" data-identity="anonymous" data-mask-url="uuid"></script>
```

Identity is set after load via `twillingate.identify("u_123")`; there is no
`data-user` in the documented examples.

## 8. Testing

| Area | Cases |
| --- | --- |
| `resolveAttributes` | five new keys resolve; unknown `$` keys still warn |
| `handlers` pageview | explicit fields win; `$url`-only falls back; neither rejects; per-field precedence |
| Legacy | the exact payload `script.js` emits still ingests and stores the same row as today |
| `CleanReferrer` | self-referral suppressed with host; taken at face value without |
| `aggregate_web` | `agg_web_hosts` rolls up; empty host bucket survives |
| Migration | `006` applies to a populated database; existing rows read back with `host = ''` |
| SDK mask resolution | built-ins, comma-separated built-ins, regexp, global function, each failure mode in §4.4 |
| SDK listeners | threading composes; `false` cancels; registration order |
| SDK routing | hash dedup registers consecutive routes; `hashchange` silent in history mode |
| `util` | `maskIds` leaves host alone, masks hash; `withQuery` sorts |
| Docs | `docs_sync_test` binds `twillingate.md` to `reservedKeys` |

`make check` (vet + coverage + restore test) gates, per `CLAUDE.md`.

## 9. Build order

1. **Server wire** (§3, §6) — additive except the `?ref=` removal.
2. **Storage** (§5) — independent of 1; may land first.
3. **Read surfaces** (§5.2) — MCP dimension, Evidence source and tile.
4. **SDK** (§4) — the largest unit; may warrant its own plan.
5. **Documentation** (§7) — the `docs/twillingate.md` consolidation, the
   repointed `docs_sync_test.go`, and the `CLAUDE.md` maintenance rule.
   The `CLAUDE.md` edit lands here rather than earlier: a rule pointing at
   a file that does not yet exist is worse than no rule.

## 10. Commit types

Per `CLAUDE.md`, only `feat`/`fix`/`perf` reach the release notes.

- `feat(server)!` — the `$host`/`$path` wire contract and the `?ref=`
  removal.
- `feat(store)` — the host dimension.
- `feat(sdk)` — masking, routing modes, `util`. Note `CLAUDE.md` does not
  list `sdk` among the scopes and the one prior commit in this area used
  `feat(script)`; `sdk` matches the tree, so `CLAUDE.md`'s scope list gains
  it alongside the §7.2 edit.
- `feat(mcpserver)` — the hosts dimension.
- `docs` — the `twillingate.md` consolidation.
