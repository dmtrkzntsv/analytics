# Ultra-Lite Self-Hosted Analytics — Design Spec

Date: 2026-08-22
Status: approved-pending-review

## 1. Overview

A lightweight, self-hosted analytics system combining **cookieless web analytics**
(Plausible-style pageviews) and **product analytics** (custom events with
attributes), for multiple projects, built around a single Go binary and SQLite.

Topology (unchanged from the original concept):

```
[ Browser / App ] --HTTP POST--> [ analytics serve (VPS, systemd) ]
                                     │  batched writes
                                     ▼
                              [ SQLite WAL DB ]
                                     │  litestream replicate (5s)
                                     ▼
                              [ Cloudflare R2 / S3 ]      ← the only bridge
                                     │  litestream restore (loop)
                                     ▼
        [ homelab: analytics sync + Evidence.dev (docker compose) ]
```

The VPS never serves queries; the homelab never accepts writes. S3/R2 is the
only channel between them (outbound HTTPS both sides).

### Non-goals

- No user accounts, admin UI, or query API — dashboards read the DB directly.
- No cookies, no persistent visitor IDs, no stored IP addresses.
- No horizontal scaling; a single VPS with SQLite is the design point
  (~15k events/s batched is far above expected load).

## 2. Dependency budget

Everything stdlib except:

- `modernc.org/sqlite` — pure-Go SQLite driver (no CGO; single static binary)
- `github.com/google/uuid` — UUIDv7 for event IDs

No YAML/config/migration/router/logging frameworks. Config is JSON
(`encoding/json`), routing is `net/http` (Go 1.22+ pattern matching), logging
is `log/slog`, migrations are embedded SQL with a ~50-line runner.

## 3. Binary & CLI (econumo-style)

One binary, `analytics`, with subcommands:

| Command | Role |
|---|---|
| `analytics serve` | VPS: HTTP ingestion + aggregation scheduler + retention pruning |
| `analytics sync` | Homelab: restore-from-R2 loop that maintains the read replica |
| `analytics migrate` | Apply pending migrations and exit (serve also migrates on boot) |
| `analytics version` | Build info |

Shipped as: (a) a static Linux binary for systemd installs, and (b) a Docker
image (multi-stage build) containing both the `analytics` binary and the
`litestream` binary (copied from the official litestream image) so `sync` can
exec it.

## 4. Configuration

`/etc/analytics/config.json`, stdlib JSON. Durations are strings parsed with
`time.ParseDuration`; days are integers.

```json
{
  "listen": "127.0.0.1:8080",
  "database": "sqlite:///var/lib/analytics/analytics.db",
  "geo": "cloudflare://",
  "log": { "level": "info", "format": "json" },
  "batch": { "max_events": 1000, "flush_interval": "5s" },
  "retention": {
    "web":     { "raw_days": 7,  "aggregate_days": 365 },
    "product": { "raw_days": 30, "aggregate_days": 365 }
  },
  "sync": {
    "interval": "5m",
    "litestream_config": "/etc/litestream.yml",
    "replica_path": "/data/analytics.db"
  },
  "projects": [
    {
      "id": "myapp",
      "name": "My App",
      "allowed_origins": ["https://myapp.com", "https://www.myapp.com"],
      "retention": { "product": { "raw_days": 60 } },
      "product_aggregation": {
        "enabled": true,
        "attributes": { "subscribed": ["plan"], "*": ["source"] },
        "top_n": 50
      }
    }
  ]
}
```

Rules:

- `database` and `geo` are DSNs; the URL scheme selects the implementation.
- Per-project `retention` overrides the global block (deep-merged).
- `product_aggregation` absent or `enabled: false` (the default) ⇒ **no product
  aggregates at all** for that project; raw product events are pruned at the
  end of the raw window with nothing rolled up.
- `attributes` maps event name → list of attribute keys to roll up; `"*"`
  applies to all events. `top_n` (default 50) caps values per (day, event,
  key); the tail collapses into `"(other)"`.
- Config is validated on boot (unknown keys warned, bad values fatal).
- Web aggregation is always on (it is the product's core value).

## 5. HTTP API (serve)

| Endpoint | Purpose |
|---|---|
| `GET /js/script.js` | Tracking snippet (~1.5 KB, cached, `Content-Type: text/javascript`) |
| `POST /api/hit` | Web pageview |
| `POST /api/event` | Product event |
| `GET /healthz` | Liveness (200 + short JSON) |

### 5.1 Payloads

```jsonc
// POST /api/hit  (sent by script.js)
{ "project_id": "myapp", "url": "https://myapp.com/pricing?utm_source=hn", "referrer": "https://news.ycombinator.com/" }

// POST /api/event
{ "project_id": "myapp", "name": "subscribed", "user_id": "u_123", "attributes": { "plan": "pro" } }
```

Limits: body ≤ 16 KB; ≤ 50 attributes; attribute keys ≤ 64 chars; values
stringified at ≤ 512 chars. Violations → 400 (attributes over-limit are
truncated, not rejected, to avoid data loss).

### 5.2 Auth model (per user decision: origin allowlist)

- The `Origin` header must match the project's `allowed_origins`
  (scheme+host exact match; `*` not supported). Mismatch → 403.
- CORS preflight (`OPTIONS`) answered only for allowed origins.
- Requests without an `Origin` header are accepted for `/api/event` only
  (server-side SDKs have no Origin). Honest limitation of the chosen model:
  origin checking deters casual browser abuse; a scripted client can spoof or
  omit it. Acceptable per the auth decision (low-stakes data, obscure URL);
  upgrading to an API key later is a small, isolated change.
- Unknown `project_id` → `204 No Content` and the event is dropped (no oracle
  for probers).

### 5.3 Ingestion pipeline

HTTP handler → validation/enrichment → buffered channel (ring, size 10k;
overflow drops oldest with a counter log) → batch worker flushing every
`flush_interval` or `max_events`, whichever first → `Store.Write*` in one
transaction. Graceful shutdown flushes the buffer (SIGTERM handler,
`systemd TimeoutStopSec=30`).

Flush errors: retried 3× with backoff (1s/5s/25s); then the batch is dropped
and an error logged with counts. Availability over completeness — ingestion
never blocks on the DB.

### 5.4 Web hit enrichment (at ingest)

- `path`, `utm_source/medium/campaign` parsed from `url`.
- `referrer_source`: cleaned referrer — own-domain referrers → `""`, known
  engines/socials normalized (`google`, `bing`, `twitter`, …, small embedded
  map), otherwise the referrer's hostname.
- `device` (desktop/mobile/tablet), `browser`, `os`: small embedded User-Agent
  matcher (~most common patterns, not a full UA DB). Known bots/crawlers/
  headless UAs are dropped before buffering.
- `country`: from the geo provider.
- `visitor_hash = SHA-256(daily_salt ‖ ip ‖ user_agent ‖ project_id)[:16]`.
  The salt lives in the `meta` table, rotates every 24 h (boot + daily job);
  the IP is used only in this computation and never stored or logged. Salt
  rotation causes a visitor-identity discontinuity at midnight UTC — accepted,
  same trade-off as Plausible.

### 5.5 Tracking script

`script.js`: auto-pageview on load, SPA support via `pushState`/`popstate`
hooks, sends `navigator.sendBeacon` (fallback `fetch` keepalive). Configured
via its script tag: `<script defer src="https://a.example.com/js/script.js"
data-project="myapp"></script>`. Ignores `localhost` and
`navigator.webdriver`. Exposes `window.analytics = { track(name, attrs) }`
which posts to `/api/event` with an anonymous per-page `user_id` unless the
site sets one via `data-user` or `analytics.identify(id)`.

## 6. Geo providers (DSN-selected)

| DSN | Behavior |
|---|---|
| `cloudflare://` | Read `CF-IPCountry` header (default; requires CF proxy in front) |
| `maxmind://LICENSE_KEY` | GeoLite2-Country lookup; DB auto-downloaded to the data dir on boot if missing or older than 30 days, refreshed weekly in-process |
| `none://` | Country always `""` |

Provider registry keyed by scheme — a new provider (e.g. `ipinfo://token`) is
one file. Lookup failures always degrade to `""`, never drop a hit.
MaxMind DB parsing is implemented against the documented MMDB format
(stdlib-only reader for the country subset) — if this proves too costly to
implement robustly, fallback decision: allow `oschwald/maxminddb-golang` as a
third dependency (flagged at implementation time).

## 7. Storage layer

### 7.1 Interface (thin; no query methods — readers hit the DB directly)

```go
type Store interface {
    Migrate(ctx context.Context) error
    SyncProjects(ctx context.Context, projects []config.Project) error
    WriteWebHits(ctx context.Context, hits []model.WebHit) error
    WriteProductEvents(ctx context.Context, events []model.ProductEvent) error
    AggregateWebDay(ctx context.Context, day civil.Date) error      // idempotent
    AggregateProductDay(ctx context.Context, day civil.Date, cfg AggCfg) error
    Prune(ctx context.Context, ret ResolvedRetention) error         // raw + agg + incremental vacuum
    RebuildFlatView(ctx context.Context) error
    GetMeta(ctx context.Context, key string) (string, error)        // salt etc.
    SetMeta(ctx context.Context, key, value string) error
    Close() error
}
```

`SyncProjects` upserts config projects and sets/clears `archived_at` per §8.
(`civil.Date` = small internal date type, not a dependency.)

Registry by DSN scheme: `sqlite://` now; `postgres://` and a Parquet/S3
backend are future work (§13). Aggregation SQL lives inside each backend.

### 7.2 SQLite specifics

- DSN: path from `sqlite:///abs/path`; opened via `modernc.org/sqlite` with
  pragmas applied on connect: `journal_mode=WAL`, `synchronous=NORMAL`,
  `busy_timeout=5000`, `cache_size=-64000`, `temp_store=MEMORY`,
  `auto_vacuum=INCREMENTAL` (set before first table creation).
- `db.SetMaxOpenConns(1)` — single-writer pipeline, no lock contention.
- Never full `VACUUM` (would force litestream to re-upload the whole DB);
  daily `PRAGMA incremental_vacuum(1000)` instead.
- `PRAGMA wal_checkpoint(TRUNCATE)` is left to litestream's checkpointing.

## 8. Database schema (migration 001)

```sql
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')));

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- keys: visitor_salt, visitor_salt_rotated_at

CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    archived_at TEXT                                    -- NULL = active
);
-- Registered/upserted from config on boot (source of truth = config).
-- Projects that disappear from the config are marked archived
-- (archived_at set); if they reappear, archived_at is cleared. Historical
-- data is never deleted by archiving; retention rules still apply.
-- Archived projects reject new events implicitly (not in config ⇒ 204-drop).

-- ===== raw: web (retained retention.web.raw_days) =====
CREATE TABLE web_hits (
    id              TEXT PRIMARY KEY,               -- UUIDv7
    project_id      TEXT NOT NULL,
    ts              TEXT NOT NULL,                  -- UTC ISO-8601
    visitor_hash    TEXT NOT NULL,
    path            TEXT NOT NULL,
    referrer_source TEXT NOT NULL DEFAULT '',
    utm_source      TEXT NOT NULL DEFAULT '',
    utm_medium      TEXT NOT NULL DEFAULT '',
    utm_campaign    TEXT NOT NULL DEFAULT '',
    country         TEXT NOT NULL DEFAULT '',
    device          TEXT NOT NULL DEFAULT '',
    browser         TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_web_hits_project_ts ON web_hits(project_id, ts);
CREATE INDEX idx_web_hits_visitor    ON web_hits(project_id, visitor_hash, ts);

-- ===== raw: product (retained retention.product.raw_days) =====
CREATE TABLE product_events (
    id         TEXT PRIMARY KEY,                    -- UUIDv7
    project_id TEXT NOT NULL,
    event_name TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    ts         TEXT NOT NULL,
    attributes TEXT NOT NULL DEFAULT '{}'           -- JSON
);
CREATE INDEX idx_events_project_name_ts ON product_events(project_id, event_name, ts);
CREATE INDEX idx_events_project_user_ts ON product_events(project_id, user_id, ts);

-- ===== web aggregates (retained retention.web.aggregate_days) =====
-- Counts, not rates: rates derive in queries; days re-aggregate safely.
CREATE TABLE agg_web_daily (
    project_id TEXT NOT NULL, day TEXT NOT NULL,    -- 'YYYY-MM-DD'
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL,
    duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project_id, day)
) WITHOUT ROWID;

-- One table per dimension, identical shape:
agg_web_pages     (project_id, day, path,    visitors, pageviews, PK(project_id, day, path))
agg_web_referrers (project_id, day, source,  visitors, pageviews, PK(project_id, day, source))
agg_web_countries (project_id, day, country, visitors, pageviews, PK(project_id, day, country))
agg_web_devices   (project_id, day, device,  visitors, pageviews, PK(project_id, day, device))
agg_web_browsers  (project_id, day, browser, visitors, pageviews, PK(project_id, day, browser))
agg_web_os        (project_id, day, os,      visitors, pageviews, PK(project_id, day, os))
agg_web_utm       (project_id, day, utm_source, utm_medium, utm_campaign,
                   visitors, pageviews, PK(project_id, day, utm_source, utm_medium, utm_campaign))
-- all WITHOUT ROWID

-- ===== product aggregates (opt-in; retained retention.product.aggregate_days) =====
CREATE TABLE agg_product_daily (
    project_id TEXT NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name)
) WITHOUT ROWID;

CREATE TABLE agg_product_totals (                    -- preserves true DAU
    project_id TEXT NOT NULL, day TEXT NOT NULL,
    total_events INTEGER NOT NULL, active_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day)
) WITHOUT ROWID;

CREATE TABLE agg_product_attrs (                     -- long format, opt-in keys
    project_id TEXT NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, event_name, attr_key, attr_value)
) WITHOUT ROWID;
```

### 8.1 Views (created/maintained outside migrations)

- **`v_events_flat`** — dynamic product-event flattener: base columns + one
  column per discovered attribute key (`attributes ->> '$.key'`). Key set
  seeded on boot via `json_each`, extended when new keys arrive in a batch.
  Keys are sanitized for the SQL alias (`[^a-zA-Z0-9_]` stripped, prefixed if
  digit-leading) **and** single-quote-escaped inside the JSON path literal;
  sanitize collisions get `_2`, `_3` suffixes. Rebuild = `DROP VIEW` +
  `CREATE VIEW` in one transaction on the writer connection.
- **Stitch views** for Evidence — hide the raw/aggregate boundary:
  `v_web_daily`, `v_web_pages`, `v_web_referrers`, `v_web_countries`,
  `v_web_devices`, `v_web_browsers`, `v_web_os`, `v_web_utm`,
  `v_product_daily`, `v_product_totals` — each `SELECT` from the aggregate
  table `UNION ALL` the same shape computed live from raw rows for days not
  yet aggregated (`day > last aggregated day`). Static SQL, created by
  migration 002.

## 9. Jobs (inside `serve`)

Scheduler goroutine; each job also runs a catch-up pass on boot so downtime
never skips work. Times UTC.

| Job | When | Action |
|---|---|---|
| Salt rotation | 00:00 daily | New random salt in `meta` |
| Aggregation | 03:00 daily | For each complete day older than the class's raw window: run `AggregateWebDay` / `AggregateProductDay` where aggregation is enabled (idempotent `INSERT OR REPLACE`), then delete that day's raw rows in the same transaction. For projects with product aggregation disabled, the raw rows are deleted without rollup |
| Pruning | 03:30 daily | Delete aggregates beyond `aggregate_days`; `PRAGMA incremental_vacuum(1000)` |
| Geo refresh | weekly | Re-download GeoLite2 if `maxmind://` |

Sessionization (during web aggregation): order hits per visitor; a gap
> 30 min splits sessions; `bounces` = single-pageview sessions;
`duration_sec` = sum of last-hit − first-hit per session.

Aggregation state is derivable (a day is "done" when it has no raw rows and
is older than the window), so no bookkeeping table is needed; re-running is
safe by construction.

## 10. Replication & homelab

### VPS
litestream (own systemd unit) replicates the DB to R2, `sync-interval: 5s`
(config identical to the original spec).

### Homelab (`analytics sync` + Evidence, docker compose)
`analytics sync` loop, every `sync.interval` (default 5 m):

1. `litestream restore -config … -o /data/.analytics.tmp.db <db>` (exec)
2. `PRAGMA quick_check` on the restored file (via the sqlite driver)
3. atomic `rename()` over `sync.replica_path`
4. update marker file `/data/.last_sync` (mtime = replica freshness)

Compose services (two): `sync` (our image, command `analytics sync`) and
`evidence` (Evidence.dev; sources refresh + static rebuild triggered when the
marker file's mtime changes, checked once a minute; serves the built site).
Evidence connects to the replica with SQLite `mode=ro`.

A starter Evidence project ships in `homelab/evidence/`: web dashboard
(visitors/pageviews trend, top pages, referrers, countries, devices, browsers,
OS, UTM campaigns — all reading `v_web_*`), product dashboard (event trends,
DAU from `v_product_totals`, per-event uniques, attribute breakdowns where
configured), project switcher driven by the `projects` table (active projects
first; archived shown separately).

## 11. Ops: install, systemd, hardening, logging

### install.sh
Interactive (with `--user NAME --yes` for automation): prompts for the service
account (suggested default `analytics`), creates it as a system user if
missing; installs the binary to `/usr/local/bin`, config skeleton to
`/etc/analytics/` (0640 root:GROUP), data dir `/var/lib/analytics` (0750,
owned by the service user); installs + enables `analytics.service` and
`litestream.service`; prints follow-up steps (edit config, add R2 creds to
`/etc/analytics/litestream.env`).

### systemd hardening (analytics.service)
`User=/Group=` from install; `NoNewPrivileges=yes`; `ProtectSystem=strict` +
`ReadWritePaths=/var/lib/analytics`; `ProtectHome=yes`; `PrivateTmp=yes`;
`PrivateDevices=yes`; `ProtectKernelTunables=yes`; `ProtectControlGroups=yes`;
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`; `RestrictNamespaces=yes`;
`LockPersonality=yes`; `CapabilityBoundingSet=`; `AmbientCapabilities=`;
`SystemCallFilter=@system-service`; `SystemCallErrorNumber=EPERM`;
`Restart=on-failure`; `TimeoutStopSec=30`. Service binds `127.0.0.1`; TLS
termination is Cloudflare/Caddy/nginx in front (documented, not installed).

### Logging
`log/slog`, JSON to stdout → journald (rotation/retention handled by
journald). Optional `log.file` config for file output; a logrotate conf ships
in `deploy/logrotate/` for that mode. No request bodies, IPs, or salts ever
logged. Per-minute summary lines (accepted/dropped/flushed counts) instead of
per-request logs.

## 12. Repository layout

```
cmd/analytics/            main + subcommand dispatch
internal/config/          JSON config, validation, DSN parsing
internal/server/          HTTP handlers, CORS/origin, limits, script.js embed
internal/pipeline/        buffered channel + batch flush worker
internal/enrich/          UA parser, referrer cleaner, UTM, bot filter
internal/geo/             provider registry: cloudflare, maxmind, none
internal/identity/        visitor hashing + salt lifecycle
internal/store/           Store interface, registry, models, date type
internal/store/sqlite/    driver, migrations (embedded), aggregation SQL, views
internal/jobs/            scheduler (aggregate, prune, salt, geo refresh)
internal/synccmd/         homelab restore loop
web/script.js             tracking snippet (embedded)
deploy/install.sh
deploy/systemd/           analytics.service, litestream.service
deploy/litestream/        litestream.yml example (VPS + restore config)
deploy/logrotate/
homelab/docker-compose.yml
homelab/evidence/         starter Evidence project
Dockerfile                multi-stage; includes litestream binary
docs/                     this spec, deployment guide
```

## 13. Future work (explicitly out of scope now)

- **Parquet/S3 storage backend** — `parquet://bucket/prefix` Store
  implementation; Evidence's DuckDB engine then reads R2 directly and the
  entire sync/restore layer disappears.
- `postgres://` Store backend (econumo parity).
- Additional geo providers (`ipinfo://` etc.).
- Real-time dashboard endpoint, funnels, retention cohorts.

## 14. Testing strategy (TDD throughout)

- **Unit**: config parsing/validation/DSN schemes; visitor hash + salt
  rotation; UA/referrer/UTM parsing; bot filter; origin/CORS matcher;
  identifier sanitization for the flat view (hostile keys: quotes, unicode,
  collisions, digit-leading).
- **Store (temp SQLite file)**: migrations idempotent; batch writes;
  `AggregateWebDay` correctness (fixtures with known sessions/bounces) and
  idempotency (run twice ⇒ identical tables); product aggregation on/off per
  config; top-N + `(other)` collapse; pruning windows; flat-view rebuild;
  stitch views return identical numbers before and after a day is aggregated.
- **HTTP (`httptest`)**: happy paths, origin rejection, unknown project 204,
  size limits, preflight.
- **Pipeline**: flush on size/interval/shutdown; overflow drop counting;
  flush retry/backoff.
- **Sync**: restore loop with a stubbed litestream exec (temp files, atomic
  swap, quick_check failure path).
```
