# App Analytics — Design Spec

Date: 2026-08-23
Status: draft-pending-review
Extends: `2026-08-22-analytics-design.md` (referred to below as "the base spec")

## 1. Overview

Extend the analytics system to cover **desktop and mobile applications** —
native mobile (iOS/Android), web-tech desktop (Electron/Tauri) and native
desktop (macOS/Windows/Linux) — alongside the existing web surface.

Apps get their own event class (`app_views`) parallel to `web_hits`, while
custom events from every surface continue to share `product_events`. The
ingest surface collapses to a **single endpoint** carrying a uniform
`{name, attributes}` event shape, authenticated by **per-client ingest keys**
that also identify the project.

### Why apps are not just web hits

The web pipeline's defining mechanism is
`visitor_hash = SHA-256(salt ‖ ip ‖ ua ‖ project)`. It exists because a
browser cannot be identified without cookies. For an app it fails hardest:
every install of a given version sends an identical User-Agent, so the hash
collapses to approximately `hash(ip)` and carrier CGNAT merges thousands of
users into one "visitor". Alongside that:

- `enrich.ParseUserAgent` classifies any client without `Mobile`/`iPad` as
  `desktop`; OkHttp, CFNetwork and Go clients are misparsed and Electron
  reports as Chrome/desktop. `enrich.IsBot` drops empty-UA requests entirely.
- `web_hits` columns `path`, `referrer_source`, `utm_*` have no app meaning.
- Apps carry dimensions web does not: **app version**, platform, OS version,
  device model, locale — and app version is the axis most app questions turn
  on.
- Apps are offline-capable, so events arrive late with client-side
  timestamps, in batches, possibly duplicated by retry.
- Apps send no `Origin`, so the existing allowlist offers them no protection.

An app *can* identify itself. Routing apps through the web pipeline means
deliberately degrading identity to a broken proxy, so apps get their own
class instead.

### Non-goals (unchanged from the base spec)

No user accounts, no admin UI, no query API. No horizontal scaling. Still one
Go binary, still SQLite, still no new dependencies.

### Deliverable

Server plus a **normative HTTP API document** (`docs/ingest-api.md`). No
client SDKs ship from this repository; the wire format is specified precisely
enough that independently written iOS, Android and desktop clients behave
identically.

## 2. Decisions

| Decision | Choice |
|---|---|
| App event class | Separate `app_views`, parallel to `web_hits` |
| Custom events | Shared `product_events` for all surfaces, plus app context columns |
| Ingest surface | One endpoint, `POST /api/events` |
| Event shape | Uniform `{id, ts, name, attributes}`; `$` prefix = system-defined |
| Auth | Per-client `ingest_keys[]`; the key resolves the project |
| Identity | Project-level `anonymous` (default) or `identified` |
| Unknown `$` names/keys | Ignored with a warning, never rejected |
| Display names | `identities` table, joined at query time; PII, opt-in |
| Retention cohorts | Included, with a bounded `actors` table |
| Rate limiting | Deferred |

## 3. Wire protocol

### 3.1 The single endpoint

`POST /api/events` replaces `/api/hit` and `/api/event`, which are **removed**.

```jsonc
{
  "key": "ak_9f3c…",
  "attributes": {                       // batch-level defaults
    "$install_id": "018f…",
    "$user_id": "u_123", "$user_name": "Ada Lovelace",
    "$group_id": "org_9", "$group_name": "Acme Corp",
    "$session_id": "018f…",
    "$platform": "ios", "$app_version": "2.4.1",
    "$os_version": "17.2", "$device_model": "iPhone15,2",
    "$locale": "en-US"
  },
  "events": [
    { "id": "018f…", "ts": "2026-08-23T10:00:00Z",
      "name": "$screen_view", "attributes": { "$screen": "/settings" } },
    { "id": "018f…", "ts": "2026-08-23T10:00:05Z",
      "name": "subscribed",
      "attributes": { "plan": "pro", "$app_version": "2.5.0" } }
  ]
}
```

Response:

```jsonc
202 { "accepted": 2, "rejected": 0, "errors": [], "warnings": [] }
```

An event is `{id, ts, name, attributes}` and nothing else. There is no second
accepted body shape — a single event is an array of one. Two accepted shapes
would mean two code paths and an ambiguous specification for the sake of a
few characters.

### 3.2 Attribute merge

Batch-level `attributes` are defaults; per-event `attributes` override them
**key by key**. This is the only merge rule, and it applies uniformly to
system (`$`) and ordinary keys alike.

The rule exists because an offline queue flushed after a week may span an app
self-update: per-event override lets a client stamp `$app_version` on the few
events that differ without grouping its queue by context. It also lets a
client stamp an ordinary attribute (`"experiment": "variant_b"`) across a
whole flush without repeating it.

### 3.3 Reserved namespace

`$` marks system-defined names and keys. The namespace is **open but
reserved**: `$` is documented as belonging to the server, but an unrecognized
`$` is tolerated rather than policed.

| Name | Routes to | Requires |
|---|---|---|
| `$pageview` | `web_hits` | `$url` |
| `$screen_view` | `app_views` | `$screen` |
| any other name | `product_events` | — |

Reserved keys:

| Group | Keys |
|---|---|
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$platform` `$app_version` `$os_version` `$device_model` `$locale` |
| Event payload | `$url` `$referrer` `$screen` |

For reserved event names, the reserved payload keys are **typed input to
enrichment, not stored attributes**: `$url` is parsed into `path` and `utm_*`
and then discarded per base spec §5.4; `$screen` populates its own column.
This is the one place the uniform shape conceals a difference, and it is
documented per reserved name.

### 3.4 Unknown `$` names and keys are ignored

- **Unknown `$` key** → dropped, not stored. Storing it would add a column to
  `v_events_flat` for a typo.
- **Unknown `$` name** → stored as an ordinary custom event under that name.
  Dropping the event would be silent data loss; this way a typo'd
  `$pageviews` appears in the dashboard as an unexpected event with a
  suspicious volume.

Both raise an entry in `warnings` (capped at 10 per response) and are counted
in the per-minute log summary.

Rejection was considered and is wrong here. Clients update on app-store
timelines while the server updates on the operator's; they will be out of
step. A client shipping a future `$session_start` against a not-yet-upgraded
server would receive a `4xx`, which §3.6 classifies as a poison batch to
drop — permanent data loss in exactly the window forward-compatibility
matters. The cost of ignoring is that typos no longer fail fast; the
`warnings` channel and dashboard visibility are the compensation.

### 3.5 Idempotency, clocks and late arrival

**Client-generated `id`.** UUIDv7, format-validated, written as the row's
primary key with `INSERT OR IGNORE`. A batch retried after a timeout that
actually succeeded dedupes server-side. An absent `id` is server-generated and
forfeits dedupe.

**Client `ts`, clamped.** Stored alongside a server `received_at`. Clamp range
is `[received_at − max_event_age, received_at + 5m]`; out-of-range values are
clamped and counted, never dropped, so a device with a broken clock still
contributes. `max_event_age` is **derived** from `RETENTION_APP_RAW_DAYS`
rather than being separately configurable — the two must agree or a clamped
event could land in an already-aggregated day, and separate knobs only invite
that misconfiguration.

**Per-event rejection, not per-batch.** A malformed event increments
`rejected` and the rest of the batch proceeds; `errors` carries
`{index, reason}` capped at 10. One bad event must not poison a 500-event
offline replay.

### 3.6 Response codes and retry semantics

| Code | Meaning | Client action |
|---|---|---|
| 202 | Accepted for processing | Do not retry |
| 400 | Malformed envelope | Drop — poison batch |
| 401 | Unknown or disabled key | Drop |
| 403 | `Origin` present and not allowed | Drop |
| 413 | Body over limit | Split and retry |
| 5xx | Server fault | Retry with backoff |

Normative: **retry only on 5xx and network failure.** Any other 4xx is a
poison batch.

The `202` is returned *before* the write — the pipeline is asynchronous — so
`accepted` counts validation, not persistence. Clients must not treat it as a
durability receipt.

### 3.7 Limits

Body ≤ 256 KB; ≤ 500 events per batch; per-event attribute limits unchanged
from base spec §5.1 (≤ 50 attributes, keys ≤ 64 chars, values stringified at
≤ 512 chars).

### 3.8 Batched `$pageview`

A hit is enriched from the connection: client IP for `country` and the
anonymous hash, User-Agent for `device`/`browser`/`os`, `Origin` for the
allowlist. Every `$pageview` in a request is therefore attributed to the
client that sent it.

This is correct for a browser batching its own pageviews and **wrong for a
backend relaying pageviews on behalf of other people**. That is a documented
prohibition, not something the server can detect.

Bot filtering applies to `$pageview` only, on the connection's User-Agent. It
never touches app or custom events, which removes the class of problem where
app clients are dropped as bots.

### 3.9 CORS and `Origin`

If `Origin` is present it must match the project's `allowed_origins`; if
absent, the request is accepted. Native apps send no `Origin` and are
unaffected. Electron and Tauri renderers do send one, so those projects add
`tauri://localhost`, `app://.` or `file://` to `allowed_origins`.

CORS responses echo the matched origin rather than `*`. Preflight is answered
with `Allow-Headers: content-type, x-analytics-key`.

The base spec is candid that `Origin` only deters *browser*-based abuse, since
a scripted client can spoof or omit it. That is precisely the case this
preserves: without the check, anyone could lift the public key off a site's
snippet and post from any page.

## 4. Authentication

### 4.1 `ingest_keys`

```jsonc
{ "alias": "myapp", "name": "My App",
  "identity": "anonymous",
  "ingest_keys": [
    { "key": "ak_9f3c…", "label": "web" },
    { "key": "ak_2b71…", "label": "ios" },
    { "key": "ak_5d04…", "label": "ios-2025", "disabled": true }
  ],
  "allowed_origins": ["https://myapp.com", "tauri://localhost"] }
```

Multiple keys per project exist so a website, an iOS app and a desktop app —
which ship on different schedules — can be retired independently.

- Any non-disabled key authenticates. Comparison is
  `subtle.ConstantTimeCompare` against each candidate, accumulated with `|`
  rather than returning early on match, so no branch varies with key content.
- `disabled: true` rather than deletion, because retirement is the step most
  likely to go wrong: flipping a bool retires a key and flipping it back
  un-retires it during a botched rollout, without regenerating and
  redistributing. Deletion is the eventual cleanup.
- A project whose keys are all disabled can receive nothing. That is a
  legitimate retired state — startup **warning**, not an error.

### 4.2 The key identifies the project

Keys are globally unique; a duplicate across projects is a fatal config
error. The server builds a key → (project, label) map at boot, so **no payload
carries a `project` field**.

Consequences:

- The "valid key for project A but payload names project B" case stops
  existing rather than being handled.
- Auth collapses to one outcome: the key resolves or it does not, and `401` is
  the only failure. The base spec's `204`-silent-drop for unknown projects
  disappears along with the oracle it created.
- `alias` becomes purely internal — the `project` column value and the
  dashboard label. It is no longer transmitted or guessable.

### 4.3 Key transport

| Surface | Transport |
|---|---|
| Browser (`script.js`) | `"key"` in the JSON body |
| Apps, server-side | `X-Analytics-Key` header (body accepted as fallback) |

The body is mandatory for browsers because `navigator.sendBeacon` **cannot set
custom headers**.

### 4.4 Rollout

`ingest_keys` is required; boot fails with a message naming each keyless
project. `analytics keygen` prints ready-to-paste keys and a complete
snippet.

The gentler alternative — key optional, warn when absent — was rejected
because it leaves a silently unauthenticated project, which is the exact
condition this change exists to remove.

## 5. Identity model

Project-level `identity`, `anonymous` by default:

| Surface | `anonymous` | `identified` |
|---|---|---|
| Web pageview | `SHA-256(salt ‖ ip ‖ ua ‖ project)` | site ID → stored visitor ID → hash fallback |
| Web custom event | `SHA-256(salt ‖ supplied_id ‖ project)` | supplied ID as-is |
| App view or event | `SHA-256(salt ‖ install_id ‖ project)` | `$install_id`/`$user_id` as-is |

One rule, no exceptions: **`anonymous` salts and rotates whatever identifier
the client supplies; `identified` stores it as given.** The salt and its 00:00
rotation are shared with the base spec's web hashing, so nothing links across
days in anonymous mode on any surface.

Resolution order for `actor_id`: `$user_id` → `$install_id` → IP+UA hash
fallback; then hashed or stored raw per mode.

`group_id` is stored **raw in both modes**. A group identifies an
organization, not a natural person, and hashing it destroys its entire
purpose — an unreadable dashboard — for no real privacy gain. The caveat that
a one-person group is effectively personal data is documented alongside the
existing PII-in-attributes note; it is the operator's call, as it already is
for `user_id`.

Anonymous mode is **strictly more accurate than the web model at identical
privacy**: web hashes IP+UA because that is all a browser offers, while an app
hashes a genuinely random `install_id`, so CGNAT no longer merges thousands of
users into one visitor. The daily rotation still destroys cross-day linkage.
The entropy source improves; the posture does not weaken.

### 5.1 Identified web visitors

`script.js` resolves the actor by precedence, highest first:

1. **Site-supplied ID** — `data-user` or `analytics.identify(userID, groupID)`.
   Stable across devices and browsers.
2. **Stored visitor ID** — UUIDv7 generated on first visit, kept in
   `localStorage`. The browser-side twin of `install_id`.
3. **Fallback** — the daily rotating hash, when storage is unavailable
   (private mode, storage blocked). Degrade, never drop.

The snippet declares `data-identity="anonymous"` or `data-identity="identified"`,
taking the same two values as `projects.json` so a mismatch is visible at a
glance. Its only job is authorizing the `localStorage` write; **the server is
the enforcement point** and an `anonymous` project salts whatever arrives
regardless of what the tag claims. A misconfigured snippet therefore fails
safe: it can waste a storage write, never leak a raw identifier into the
database.

```html
<script defer src="https://a.example.com/js/script.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

`analytics.reset()` is added and is **not optional** — without it the next
person to use a shared browser inherits the previous user's identity.

### 5.2 Privacy posture change

A persistent `localStorage` identifier is terminal-equipment storage under
ePrivacy — the same legal category as a cookie, whatever the technical
mechanism. Base spec §5.4a's claim to match Plausible's consent-free model
**remains true for `anonymous` projects and becomes false for `identified`
ones.** The documentation must state this in those words rather than leaving
an operator to infer it.

Consent gating is the operator's responsibility; withholding `data-identity`
until consent is the intended hook.

`$user_name` and `$group_name` (§8) store display names, which are PII by
definition. They are opt-in per event and `$user_name` is ignored entirely in
anonymous mode.

### 5.3 Late identification

`analytics.identify(userID, groupID)` persists both to `localStorage`, so
every subsequent event — this page and all future page loads — carries them.
`data-user` and `data-group` cover server-rendered pages that already know at
load time.

**Limitation, stated rather than engineered around:** a pageview fired at load,
before `identify()` runs, is stored without `$user_id`/`$group_id` and stays
that way. There is no retroactive stitching. Doing it properly needs an
actor → user mapping that survives raw pruning, and in anonymous mode it is
forbidden by construction. In practice an app that identifies on boot loses
only the landing pageview.

## 6. Schema

### 6.1 Migration 003 — tables

Views are dropped first, tables altered, then views recreated by migration
004; SQLite rewrites schema text on `RENAME COLUMN` and the existing views
reference the renamed columns.

```sql
-- projects gains the identity mode so dashboards can branch on it
ALTER TABLE projects ADD COLUMN identity TEXT NOT NULL DEFAULT 'anonymous';

-- ===== web_hits: rename + identity columns =====
ALTER TABLE web_hits RENAME COLUMN visitor_hash TO actor_id;
ALTER TABLE web_hits ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== product_events: rename + identity and app context =====
ALTER TABLE product_events RENAME COLUMN user_id TO actor_id;
ALTER TABLE product_events ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN platform    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== raw: app views =====
CREATE TABLE app_views (
    id           TEXT PRIMARY KEY,          -- client-generated UUIDv7
    project      TEXT NOT NULL,
    ts           TEXT NOT NULL,             -- client clock, clamped
    received_at  TEXT NOT NULL,             -- server clock
    actor_id     TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
    group_id     TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    screen       TEXT NOT NULL,
    platform     TEXT NOT NULL DEFAULT '',
    app_version  TEXT NOT NULL DEFAULT '',
    os_version   TEXT NOT NULL DEFAULT '',
    device_model TEXT NOT NULL DEFAULT '',
    locale       TEXT NOT NULL DEFAULT '',
    country      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_app_views_project_ts    ON app_views(project, ts);
CREATE INDEX idx_app_views_actor         ON app_views(project, actor_id, ts);
CREATE INDEX idx_app_views_session       ON app_views(project, session_id, ts);
```

Note the absences in `app_views`: no `browser`, no `device` class, no
`referrer_source`, no `utm_*`, and **no User-Agent parsing anywhere**.
`platform`, `os_version` and `device_model` are declared by the client, which
is why the Electron-reports-as-Chrome and OkHttp-reports-as-desktop problems
disappear rather than being patched.

`os_version`, `device_model` and `locale` are deliberately **not** copied onto
`product_events`; only `platform` and `app_version` are. Within the raw window
they can be joined from `app_views`, and "conversions by device model" is not
an expected question. Additive later if that proves wrong.

### 6.2 Migration 003 — app aggregates

```sql
CREATE TABLE agg_app_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day)
) WITHOUT ROWID;

agg_app_screens   (project, day, screen,                actives, views)
agg_app_versions  (project, day, platform, app_version, actives, views)
agg_app_os        (project, day, platform, os_version,  actives, views)
agg_app_devices   (project, day, device_model,          actives, views)
agg_app_countries (project, day, country,               actives, views)
-- all WITHOUT ROWID, primary key = (project, day, <dimension columns>)
```

`platform` is in the key of `versions` and `os` because "2.4.1" and "17.2"
mean unrelated things across iOS and Android; without it the rollup silently
merges separate releases.

**No `bounces`.** A single-screen session is normal app use, not a bounce; the
metric would be noise. Web keeps it.

### 6.3 Identity aggregates and names

```sql
CREATE TABLE identities (
    project        TEXT NOT NULL,
    kind           TEXT NOT NULL,          -- 'user' | 'group'
    id             TEXT NOT NULL,
    name           TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (project, kind, id)
) WITHOUT ROWID;

CREATE TABLE agg_identity_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    hits INTEGER NOT NULL, views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind, id)
) WITHOUT ROWID;
```

Display names live in `identities` rather than on event rows: a name repeated
on every row could never be updated, and names do change. Latest write wins.
`users` is meaningful for `kind='group'` (distinct users in that group that
day) and is always 1 for `kind='user'`.

`identities` is evicted on the same rule as `actors` (§7): rows whose
`last_seen_day` falls outside `aggregate_days` are deleted.

### 6.4 Migration 004 — views

`v_app_daily`, `v_app_screens`, `v_app_versions`, `v_app_os`,
`v_app_devices`, `v_app_countries`, `v_identity_daily`, `v_retention` follow
the `v_web_*` pattern from migration 002: the aggregate table `UNION ALL` the
same shape computed live from raw rows for days not yet aggregated, so
Evidence never sees the raw/aggregate boundary. `v_events_flat` is recreated
against the renamed `actor_id` column.

`v_retention` is not stitched — retention is defined only over aggregated
days — and instead exposes `cohort_day`, `day_offset`, `actors` and the
offset-0 cohort size for rate computation.

## 7. Retention cohorts

```sql
CREATE TABLE actors (
    project TEXT NOT NULL, actor_id TEXT NOT NULL,
    surface TEXT NOT NULL,               -- 'web' | 'app', where first seen
    first_seen_day TEXT NOT NULL,        -- the cohort
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project, actor_id)
) WITHOUT ROWID;
CREATE INDEX idx_actors_last_seen ON actors(project, last_seen_day);

CREATE TABLE agg_retention (
    project TEXT NOT NULL, surface TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project, surface, cohort_day, day_offset)
) WITHOUT ROWID;
```

`surface` is in the key because a web visitor ID and an app `install_id` are
different actors even for the same human, and web retention curves sit far
below app curves — blending them describes neither population.

**Computation, during the 03:00 pass for day D.** Upsert `actors`
(min `first_seen_day`, max `last_seen_day`) from day D's raw rows, then:

```sql
INSERT OR REPLACE INTO agg_retention
SELECT project, surface, first_seen_day,
       CAST(julianday(D) - julianday(first_seen_day) AS INTEGER),
       COUNT(DISTINCT actor_id)
FROM <raw rows for D> JOIN actors USING (project, actor_id)
GROUP BY 1, 2, 3;
```

Every `(cohort, offset)` pair is produced by exactly one day — `D = cohort +
offset` — so `REPLACE` is a full recompute of precisely the rows that day
owns. Re-running a day is safe by construction, matching every other
aggregate. Days are processed chronologically, so `first_seen_day` is always
established before a later day references it. Because the computation happens
while day D's raw rows are still present, no per-day activity history is
stored.

**Bounded.** `actors` rows are evicted once `last_seen_day` falls outside
`aggregate_days` (365), sizing the table by yearly-active actors rather than
all-time. A user returning after 366 days counts as new — approximate at the
long tail in exchange for predictable storage. At roughly 60 bytes per row, a
million yearly-active actors is about 60 MB; this is the only table in the
system not bounded by a day window, which matters on the SD-card hardware
target of base spec §12a.

**Skipped for `anonymous` projects.** `actor_id` rotates at midnight, so
`first_seen_day` always equals D and every cohort would hold nothing but
offset 0. Retention is genuinely undefined under daily rotation; the job
checks `identity` and moves on.

## 8. Aggregation and jobs

The 03:00 daily pass gains a third event class and two new steps. Boot
catch-up is unchanged.

| Step | Action |
|---|---|
| Aggregate app day | `agg_app_*` from `app_views` |
| Upsert actors | `actors` first/last seen from all three raw tables |
| Aggregate retention | `agg_retention` for day D (identified projects only) |
| Aggregate identities | `agg_identity_daily`; upsert `identities.last_seen_day` |
| Prune | app aggregates, `actors`, `identities` beyond their windows |

**Sessions.** Client `$session_id` is authoritative when present — the app
knows its own foreground/background transitions, which beats inference. When
absent, the existing 30-minute-gap sessionization over `actor_id` is reused,
not reimplemented. A session is attributed to the day of its first view, with
its full duration. In anonymous mode `actor_id` rotates at 00:00, so
gap-inferred sessions cannot cross midnight by construction; a client-declared
one can.

**Cardinality cap.** `screen`, `device_model` and `agg_identity_daily.id` are
client-supplied free strings. Each gets a fixed top-500 cap per day with the
tail collapsed into `(other)`, reusing the product side's existing top-N SQL.
Web's `path` remains uncapped — existing behaviour, unchanged here — but an
unbounded *new* dimension is the wrong default on Pi-class hardware. The
"don't put IDs in screen names" guidance joins the existing PII-in-paths note.

**Store interface** gains `WriteAppViews`, `AppDaysBefore`, `AggregateAppDay`,
`UpsertActors`, `AggregateRetentionDay`, `AggregateIdentityDay`,
`UpsertIdentities`, `PruneActors`, `PruneIdentities`; `PruneAggregates` gains
an `appBefore` parameter.

## 9. Configuration

`projects.json`:

```jsonc
[{ "alias": "myapp", "name": "My App",
   "identity": "anonymous",
   "ingest_keys": [ { "key": "ak_9f3c…", "label": "web" } ],
   "allowed_origins": ["https://myapp.com", "tauri://localhost"],
   "product_aggregation": { "enabled": true, "attributes": {"*": ["source"]} }
}]
```

`.env` gains only:

```
#RETENTION_APP_RAW_DAYS=30
#RETENTION_APP_AGGREGATE_DAYS=365
```

App raw defaults to 30 days rather than web's 7 because it doubles as the
late-arrival window — the same number as `max_event_age`, which is what
guarantees a clamped event can never target an already-aggregated day. A phone
offline for three weeks still lands correctly.

There is deliberately **no `app.enabled` flag**. Unlike `product_aggregation`,
which is opt-in because attribute rollup is high-cardinality and expensive,
the app dimensions are a fixed set, so aggregating a project with zero
`app_views` is a no-op over an empty table.

Batch limits are compile-time constants.

## 10. Dashboards

New Evidence pages reading the stitch views:

**App** — actives and views trend; sessions and average duration; top screens;
platform split; OS versions; device models; countries. The chart to build
deliberately is **version adoption**: stacked area over `agg_app_versions`,
which is how a release rollout is actually watched and how the version where a
metric fell off is spotted.

**Users** — active users trend (DAU/WAU/MAU); top users by activity with
display name and last seen; new versus returning; retention curve.

**Groups** — active groups trend; top groups by activity with display name;
users per group; group drill-down showing activity over time and top events.

**Retention** — cohort curve and the cohort triangle, per surface.

The Users and Groups pages are meaningful only for `identified` projects — in
anonymous mode `user_id` is a rotating hash and a leaderboard of hashes is
nonsense. Pages branch on `projects.identity`, which is why §6.1 adds that
column. The existing project switcher needs no change.

## 11. Breaking changes

1. **`ingest_keys` required** — boot fails until every project has one.
   `analytics keygen` prints them.
2. **Every embedded snippet must be rewritten** — `data-key` and
   `data-identity` replace `data-project`. Old snippets stop working. Server
   and site snippets must ship together; this is a coordinated deploy and is
   the direct cost of keying the web surface.
3. **`/api/hit` and `/api/event` are removed**, not deprecated.
4. **Unknown project returns 401**, not 204.
5. **`identity: "anonymous"` now salts site-supplied web `user_id`.** A site
   calling `analytics.identify("u_123")` previously wrote `u_123` straight
   into `product_events.user_id` — a persistent cross-day identifier governed
   by nothing. Under the new default it is salted and rotated, so **cross-day
   web funnels and any query joining product events across days stop working**
   until that project sets `identity: "identified"`. This is a behaviour
   change on data that may already be collected, not a config rename.
6. **`Store.PruneAggregates` signature** gains a parameter (internal only).

## 12. Testing

Following base spec §14; coverage floors unchanged (85% for core packages).
New-risk cases:

- **Wire**: attribute merge precedence, batch default versus per-event
  override, unknown `$` name stored as custom event, unknown `$` key dropped,
  `warnings` capping, per-event rejection leaving the batch intact.
- **Idempotency**: replayed batch dedupes via `INSERT OR IGNORE`; missing `id`
  falls back to server generation.
- **Clocks**: far-past, far-future and broken-clock timestamps clamp to the
  window and are counted; a clamped event never lands in an aggregated day.
- **Auth**: constant-time compare; disabled key rejected; all-disabled project
  warns at boot; duplicate key across projects is fatal; key resolves project
  without a payload field.
- **Identity**: both modes across all three surfaces; `group_id` raw in both;
  `$user_name` ignored in anonymous mode; server enforcement overriding a
  misconfigured `data-identity`.
- **Origin**: present-and-allowed, present-and-denied, absent-and-accepted.
- **Aggregation**: app-day correctness and idempotency; top-500 `(other)`
  collapse; client `$session_id` versus gap-inferred sessions; session
  spanning midnight.
- **Retention**: cohort computation, re-running a day yields identical rows,
  chronological ordering of `first_seen_day`, eviction at the boundary,
  anonymous projects skipped.
- **Views**: stitch views return identical numbers before and after a day is
  aggregated.

## 13. Repository impact

```
internal/server/       events handler (replaces hit/event handlers)
internal/config/       ingest_keys, identity, key->project map
internal/store/        interface additions, AppView model
internal/store/sqlite/ aggregate_app.go, retention.go, identities.go,
                       migrations 003 + 004
web/script.js          data-key, data-identity, identify/reset, localStorage
backoffice/evidence/   app, users, groups, retention pages
docs/ingest-api.md     normative wire format (new, first-class artifact)
```

No new packages, no new dependencies. `internal/enrich` is untouched — apps
declare their context rather than having it parsed from a User-Agent.

## 14. Deferred

- **Rate limiting** — per-key and per-IP token buckets. The key already
  provides revocation, which is the part that matters.
- **Retroactive first-pageview attribution** — requires an actor → user
  mapping surviving raw pruning; see §5.3.
- **Cross-surface identity stitching** — linking a web visitor to an app
  install for the same human.
