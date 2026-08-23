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
         COUNT(DISTINCT visitor_hash) AS visitors, COUNT(*) AS pageviews
  FROM web_hits GROUP BY project, substr(ts,1,10)
) c
JOIN (
  WITH base AS (
    SELECT project, substr(ts,1,10) AS day, visitor_hash,
           CAST(strftime('%s', ts) AS INTEGER) AS t
    FROM web_hits
  ),
  marked AS (
    SELECT project, day, visitor_hash, t,
           CASE WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1 ELSE 0 END AS new_session
    FROM base WINDOW w AS (PARTITION BY project, day, visitor_hash ORDER BY t)
  ),
  numbered AS (
    SELECT project, day, visitor_hash, t,
           SUM(new_session) OVER (PARTITION BY project, day, visitor_hash ORDER BY t) AS session_no
    FROM marked
  ),
  per_session AS (
    SELECT project, day, visitor_hash, session_no,
           COUNT(*) AS hit_count, MAX(t) - MIN(t) AS duration
    FROM numbered GROUP BY project, day, visitor_hash, session_no
  )
  SELECT project, day, COUNT(*) AS sessions,
         SUM(CASE WHEN hit_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(duration), 0) AS duration_sec
  FROM per_session GROUP BY project, day
) p ON p.project = c.project AND p.day = c.day;

CREATE VIEW v_web_pages AS
SELECT project, day, path, visitors, pageviews FROM agg_web_pages
UNION ALL
SELECT project, substr(ts,1,10), path, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), path;

CREATE VIEW v_web_referrers AS
SELECT project, day, source, visitors, pageviews FROM agg_web_referrers
UNION ALL
SELECT project, substr(ts,1,10), referrer_source, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), referrer_source;

CREATE VIEW v_web_countries AS
SELECT project, day, country, visitors, pageviews FROM agg_web_countries
UNION ALL
SELECT project, substr(ts,1,10), country, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), country;

CREATE VIEW v_web_devices AS
SELECT project, day, device, visitors, pageviews FROM agg_web_devices
UNION ALL
SELECT project, substr(ts,1,10), device, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), device;

CREATE VIEW v_web_browsers AS
SELECT project, day, browser, visitors, pageviews FROM agg_web_browsers
UNION ALL
SELECT project, substr(ts,1,10), browser, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), browser;

CREATE VIEW v_web_os AS
SELECT project, day, os, visitors, pageviews FROM agg_web_os
UNION ALL
SELECT project, substr(ts,1,10), os, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), os;

CREATE VIEW v_web_utm AS
SELECT project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews FROM agg_web_utm
UNION ALL
SELECT project, substr(ts,1,10), utm_source, utm_medium, utm_campaign,
       COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits
WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
GROUP BY project, substr(ts,1,10), utm_source, utm_medium, utm_campaign;

CREATE VIEW v_product_daily AS
SELECT project, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT user_id)
FROM product_events GROUP BY project, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project, substr(ts,1,10), COUNT(*), COUNT(DISTINCT user_id)
FROM product_events GROUP BY project, substr(ts,1,10);
