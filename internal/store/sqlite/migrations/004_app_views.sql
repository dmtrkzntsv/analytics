-- Recreated after migration 003's column renames, plus the app family.
-- Stitch views: aggregates UNION ALL live computation over raw rows.
-- Raw and aggregated days are disjoint (aggregation deletes raw in-tx),
-- so no day is double counted.
--
-- The live halves must stay numerically identical to the corresponding
-- INSERTs in aggregate_web.go / aggregate_product.go; if they drift, a
-- dashboard would jump the moment a day gets aggregated. The invariant
-- tests in views_test.go exist to catch exactly that.

CREATE VIEW v_web_daily AS
SELECT project, day, visitors, pageviews, sessions, bounces, duration_sec FROM agg_web_daily
UNION ALL
SELECT c.project, c.day, c.visitors, c.pageviews, p.sessions, p.bounces, p.duration_sec
FROM (
  SELECT project, substr(ts,1,10) AS day,
         COUNT(DISTINCT actor_id) AS visitors, COUNT(*) AS pageviews
  FROM web_hits GROUP BY project, substr(ts,1,10)
) c
JOIN (
  WITH base AS (
    SELECT project, substr(ts,1,10) AS day, actor_id,
           CAST(strftime('%s', ts) AS INTEGER) AS t
    FROM web_hits
  ),
  marked AS (
    SELECT project, day, actor_id, t,
           CASE WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1 ELSE 0 END AS new_session
    FROM base WINDOW w AS (PARTITION BY project, day, actor_id ORDER BY t)
  ),
  numbered AS (
    SELECT project, day, actor_id, t,
           SUM(new_session) OVER (PARTITION BY project, day, actor_id ORDER BY t) AS session_no
    FROM marked
  ),
  per_session AS (
    SELECT project, day, actor_id, session_no,
           COUNT(*) AS hit_count, MAX(t) - MIN(t) AS duration
    FROM numbered GROUP BY project, day, actor_id, session_no
  )
  SELECT project, day, COUNT(*) AS sessions,
         SUM(CASE WHEN hit_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(duration), 0) AS duration_sec
  FROM per_session GROUP BY project, day
) p ON p.project = c.project AND p.day = c.day;

CREATE VIEW v_web_pages AS
SELECT project, day, path, visitors, pageviews FROM agg_web_pages
UNION ALL
SELECT project, substr(ts,1,10), path, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), path;

CREATE VIEW v_web_referrers AS
SELECT project, day, source, visitors, pageviews FROM agg_web_referrers
UNION ALL
SELECT project, substr(ts,1,10), referrer_source, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), referrer_source;

CREATE VIEW v_web_countries AS
SELECT project, day, country, visitors, pageviews FROM agg_web_countries
UNION ALL
SELECT project, substr(ts,1,10), country, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), country;

CREATE VIEW v_web_devices AS
SELECT project, day, device, visitors, pageviews FROM agg_web_devices
UNION ALL
SELECT project, substr(ts,1,10), device, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), device;

CREATE VIEW v_web_browsers AS
SELECT project, day, browser, visitors, pageviews FROM agg_web_browsers
UNION ALL
SELECT project, substr(ts,1,10), browser, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), browser;

CREATE VIEW v_web_os AS
SELECT project, day, os, visitors, pageviews FROM agg_web_os
UNION ALL
SELECT project, substr(ts,1,10), os, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), os;

CREATE VIEW v_web_utm AS
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews FROM agg_web_utm
UNION ALL
SELECT project, substr(ts,1,10), utm_source, utm_medium, utm_campaign,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits
WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
GROUP BY project, substr(ts,1,10), utm_source, utm_medium, utm_campaign;

CREATE VIEW v_product_daily AS
SELECT project, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project, substr(ts,1,10), COUNT(*), COUNT(DISTINCT actor_id)
FROM product_events GROUP BY project, substr(ts,1,10);


-- ===== app stitch views (app-analytics spec §6.4) =====
-- Aggregate table UNION ALL the same shape computed live from raw rows for
-- days not yet aggregated. The live halves must stay numerically identical
-- to aggregate_app.go; views_test.go enforces that.

CREATE VIEW v_app_daily AS
SELECT project, day, actives, views, sessions, duration_sec FROM agg_app_daily
UNION ALL
SELECT c.project, c.day, c.actives, c.views, s.sessions, s.duration_sec
FROM (
  SELECT project, substr(ts,1,10) AS day,
         COUNT(DISTINCT actor_id) AS actives, COUNT(*) AS views
  FROM app_views GROUP BY project, substr(ts,1,10)
) c
JOIN (
  WITH base AS (
    SELECT project, substr(ts,1,10) AS day, actor_id, session_id,
           CAST(strftime('%s', ts) AS INTEGER) AS t
    FROM app_views
  ),
  marked AS (
    SELECT project, day, actor_id, session_id, t,
           CASE WHEN session_id <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM base WINDOW w AS (PARTITION BY project, day, actor_id ORDER BY t)
  ),
  numbered AS (
    SELECT project, day, actor_id, t,
           CASE WHEN session_id <> '' THEN session_id
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project, day, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project, day, actor_id, skey, MAX(t) - MIN(t) AS dur
    FROM numbered GROUP BY project, day, actor_id, skey
  )
  SELECT project, day, COUNT(*) AS sessions, SUM(dur) AS duration_sec
  FROM spans GROUP BY project, day
) s ON s.project = c.project AND s.day = c.day;

CREATE VIEW v_app_screens AS
SELECT project, day, screen, actives, views FROM agg_app_screens
UNION ALL
SELECT project, substr(ts,1,10), screen, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), screen;

CREATE VIEW v_app_versions AS
SELECT project, day, platform, app_version, actives, views FROM agg_app_versions
UNION ALL
SELECT project, substr(ts,1,10), platform, app_version, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), platform, app_version;

CREATE VIEW v_app_os AS
SELECT project, day, platform, os_version, actives, views FROM agg_app_os
UNION ALL
SELECT project, substr(ts,1,10), platform, os_version, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), platform, os_version;

CREATE VIEW v_app_devices AS
SELECT project, day, device_model, actives, views FROM agg_app_devices
UNION ALL
SELECT project, substr(ts,1,10), device_model, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), device_model;

CREATE VIEW v_app_countries AS
SELECT project, day, country, actives, views FROM agg_app_countries
UNION ALL
SELECT project, substr(ts,1,10), country, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), country;

CREATE VIEW v_identity_daily AS
SELECT project, day, kind, id, actors, users, hits, views, events
FROM agg_identity_daily;

-- Retention is defined only over aggregated days, so there is no live half.
-- cohort_size is the offset-0 row, exposed so rates compute in one query.
CREATE VIEW v_retention AS
SELECT r.project, r.surface, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project = r.project AND c.surface = r.surface
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;
