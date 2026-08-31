-- Host was computed from $url and discarded: it existed only to suppress
-- self-referrals. Storing it lets a project spanning a marketing site and
-- an app keep their /pricing rows apart instead of collapsing them.
--
-- Rows written before this migration carry host='' and cannot be
-- backfilled -- the value was never persisted. The empty bucket in
-- v_web_hosts is that history, not a bug.
ALTER TABLE web_hits ADD COLUMN host TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_web_hosts (
    project TEXT NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, host)
) WITHOUT ROWID;

-- Mirrors v_web_pages (004_app_views.sql:47): aggregated history stitched
-- to a live half over raw rows, so today and yesterday are included. Raw
-- and aggregated days are disjoint (aggregation deletes raw in the same
-- transaction), so the live half needs no day exclusion.
CREATE VIEW v_web_hosts AS
SELECT project, day, host, visitors, pageviews FROM agg_web_hosts
UNION ALL
SELECT project, substr(ts,1,10), host, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), host;
