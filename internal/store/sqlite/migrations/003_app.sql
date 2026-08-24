-- App analytics (app-analytics spec §6). Views are dropped here and
-- recreated by 004: RENAME COLUMN must not leave a stale view definition
-- behind, and SQLite rewrites dependent schema text in place.

DROP VIEW IF EXISTS v_web_daily;
DROP VIEW IF EXISTS v_web_pages;
DROP VIEW IF EXISTS v_web_referrers;
DROP VIEW IF EXISTS v_web_countries;
DROP VIEW IF EXISTS v_web_devices;
DROP VIEW IF EXISTS v_web_browsers;
DROP VIEW IF EXISTS v_web_os;
DROP VIEW IF EXISTS v_web_utm;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_events_flat;

ALTER TABLE projects ADD COLUMN identity TEXT NOT NULL DEFAULT 'anonymous';

-- ===== web_hits: rename + identity columns =====
-- visitor_hash would hold a raw account id in identified mode; a column
-- named "_hash" containing plaintext is how privacy incidents happen.
ALTER TABLE web_hits RENAME COLUMN visitor_hash TO actor_id;
ALTER TABLE web_hits ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== product_events: rename + identity and app context =====
-- The old user_id always meant "the actor", so it becomes actor_id; the new
-- user_id is the explicitly supplied account identity.
ALTER TABLE product_events RENAME COLUMN user_id TO actor_id;
ALTER TABLE product_events ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN platform    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== raw: app views =====
-- No browser, device class, referrer or utm columns, and no User-Agent
-- parsing anywhere: apps declare their context.
CREATE TABLE app_views (
    id           TEXT PRIMARY KEY,
    project      TEXT NOT NULL,
    ts           TEXT NOT NULL,
    received_at  TEXT NOT NULL,
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
CREATE INDEX idx_app_views_project_ts ON app_views(project, ts);
CREATE INDEX idx_app_views_actor      ON app_views(project, actor_id, ts);
CREATE INDEX idx_app_views_session    ON app_views(project, session_id, ts);

-- ===== app aggregates =====
-- Counts, not rates. No "bounces": a single-screen session is normal app
-- use, so the metric would be noise.
CREATE TABLE agg_app_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day)
) WITHOUT ROWID;

CREATE TABLE agg_app_screens (
    project TEXT NOT NULL, day TEXT NOT NULL, screen TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, screen)
) WITHOUT ROWID;

-- platform is in the key: "2.4.1" means unrelated things across iOS and
-- Android, and without it the rollup silently merges separate releases.
CREATE TABLE agg_app_versions (
    project TEXT NOT NULL, day TEXT NOT NULL,
    platform TEXT NOT NULL, app_version TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform, app_version)
) WITHOUT ROWID;

CREATE TABLE agg_app_os (
    project TEXT NOT NULL, day TEXT NOT NULL,
    platform TEXT NOT NULL, os_version TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform, os_version)
) WITHOUT ROWID;

CREATE TABLE agg_app_devices (
    project TEXT NOT NULL, day TEXT NOT NULL, device_model TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, device_model)
) WITHOUT ROWID;

CREATE TABLE agg_app_countries (
    project TEXT NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, country)
) WITHOUT ROWID;

-- ===== cohorts =====
-- surface is in the key: a web visitor id and an app install_id are
-- different actors even for the same human, and their curves describe
-- different populations.
CREATE TABLE actors (
    project TEXT NOT NULL, actor_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
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

-- ===== identities and their aggregates =====
-- Display names live here rather than on event rows: a name repeated on
-- every row could never be updated, and names change.
CREATE TABLE identities (
    project       TEXT NOT NULL,
    kind          TEXT NOT NULL,
    id            TEXT NOT NULL,
    name          TEXT NOT NULL,
    last_seen_day TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (project, kind, id)
) WITHOUT ROWID;

CREATE TABLE agg_identity_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    hits INTEGER NOT NULL, views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind, id)
) WITHOUT ROWID;
