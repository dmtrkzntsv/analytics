CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- keys: visitor_salt, visitor_salt_rotated_at

CREATE TABLE projects (
    id          TEXT PRIMARY KEY,                       -- UUIDv7, generated at first registration
    alias       TEXT NOT NULL UNIQUE,                   -- the config/tracking key
    name        TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    archived_at TEXT                                    -- NULL = active
);
-- Registered/upserted from config on boot, matched by alias (source of
-- truth = config; the id is generated once — UUIDv7 — and never changes).
-- Config and tracking payloads use the alias everywhere; event and
-- aggregate rows store the alias in their `project` column. Projects that
-- disappear from the config are marked archived (archived_at set); if they
-- reappear (same alias), archived_at is cleared and the id is retained.
-- Historical data is never deleted by archiving; retention rules still
-- apply. Archived projects reject new events implicitly (not in config ⇒
-- 204-drop).

-- ===== raw: web (retained retention.web.raw_days) =====
CREATE TABLE web_hits (
    id              TEXT PRIMARY KEY,               -- UUIDv7
    project      TEXT NOT NULL,
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
CREATE INDEX idx_web_hits_project_ts ON web_hits(project, ts);
CREATE INDEX idx_web_hits_visitor    ON web_hits(project, visitor_hash, ts);

-- ===== raw: product (retained retention.product.raw_days) =====
CREATE TABLE product_events (
    id         TEXT PRIMARY KEY,                    -- UUIDv7
    project TEXT NOT NULL,
    event_name TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    ts         TEXT NOT NULL,
    attributes TEXT NOT NULL DEFAULT '{}'           -- JSON
);
CREATE INDEX idx_events_project_name_ts ON product_events(project, event_name, ts);
CREATE INDEX idx_events_project_user_ts ON product_events(project, user_id, ts);

-- ===== web aggregates (retained retention.web.aggregate_days) =====
-- Counts, not rates: rates derive in queries; days re-aggregate safely.
CREATE TABLE agg_web_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,    -- 'YYYY-MM-DD'
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    sessions INTEGER NOT NULL, bounces INTEGER NOT NULL,
    duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day)
) WITHOUT ROWID;

-- One table per dimension, identical shape:
CREATE TABLE agg_web_pages (
    project TEXT NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, path)
) WITHOUT ROWID;

CREATE TABLE agg_web_referrers (
    project TEXT NOT NULL, day TEXT NOT NULL, source TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, source)
) WITHOUT ROWID;

CREATE TABLE agg_web_countries (
    project TEXT NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, country)
) WITHOUT ROWID;

CREATE TABLE agg_web_devices (
    project TEXT NOT NULL, day TEXT NOT NULL, device TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, device)
) WITHOUT ROWID;

CREATE TABLE agg_web_browsers (
    project TEXT NOT NULL, day TEXT NOT NULL, browser TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, browser)
) WITHOUT ROWID;

CREATE TABLE agg_web_os (
    project TEXT NOT NULL, day TEXT NOT NULL, os TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, os)
) WITHOUT ROWID;

CREATE TABLE agg_web_utm (
    project TEXT NOT NULL, day TEXT NOT NULL,
    utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, utm_source, utm_medium, utm_campaign)
) WITHOUT ROWID;
-- all WITHOUT ROWID

-- ===== product aggregates (opt-in; retained retention.product.aggregate_days) =====
CREATE TABLE agg_product_daily (
    project TEXT NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project, day, event_name)
) WITHOUT ROWID;

CREATE TABLE agg_product_totals (                    -- preserves true DAU
    project TEXT NOT NULL, day TEXT NOT NULL,
    total_events INTEGER NOT NULL, active_users INTEGER NOT NULL,
    PRIMARY KEY (project, day)
) WITHOUT ROWID;

CREATE TABLE agg_product_attrs (                     -- long format, opt-in keys
    project TEXT NOT NULL, day TEXT NOT NULL, event_name TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    count INTEGER NOT NULL, unique_users INTEGER NOT NULL,
    PRIMARY KEY (project, day, event_name, attr_key, attr_value)
) WITHOUT ROWID;
