-- Stitch view for attribute breakdowns. agg_product_attrs was the only
-- aggregate table without one, so breakdowns only appeared once a day was
-- rolled up -- up to product.raw_days stale while v_product_daily showed
-- today.
--
-- 002_views.sql:5-8 binds this: the live half must be numerically
-- identical to what rollupAttrValue in aggregate_product.go writes, or the
-- table jumps the moment a day ages over into aggregates. Everything below
-- mirrors that function -- the ranking and its count-desc/value-asc
-- tiebreak, the rn <= cap cutoff, and the "(other)" tail whose
-- unique_users is recomputed from raw rather than summed. Raw and
-- aggregated days are disjoint (aggregation deletes raw in the same
-- transaction), so the live half needs no day exclusion.
--
-- Both inputs are reachable from SQL, so this is static: the declared keys
-- live in projects.attributes and the cardinality cap is written to meta
-- at boot by internal/app.
CREATE VIEW v_product_attrs AS
WITH cap AS (
  -- The cardinality cap, resolved exactly the way AggregateProductDay
  -- resolves it (aggregate_product.go:29-31): a non-positive topN clamps
  -- to defaultAttrsTopN rather than erroring, because `rn <= 0` keeps
  -- nothing and would sweep every value into "(other)" -- a permanent,
  -- undetected loss of breakdowns. The WHERE does the clamping: a missing
  -- row, a non-positive one, and a non-numeric one (CAST yields 0) all
  -- select no row, so COALESCE returns the default.
  --
  -- This SELECT has no FROM, so it yields exactly one row always, and it
  -- is read below as a scalar subquery rather than cross-joined. Both
  -- matter: a cap relation that could be empty would silently drop the
  -- entire live half instead of degrading to the default.
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  -- DISTINCT because a duplicated key in the JSON array would join each
  -- event row twice and double its counts, where the aggregation's
  -- INSERT OR REPLACE just writes the same row twice. json_valid guards a
  -- hand-edited row from erroring every query (same reasoning as 006).
  SELECT DISTINCT p.alias AS project, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  -- One row per (event, attr_key) occurrence with a present value. The
  -- path is built the way attrPath() builds it in Go, backslash-escaping
  -- embedded quotes, so an exotic key resolves identically on both sides.
  SELECT pe.project AS project, substr(pe.ts,1,10) AS day,
         pe.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(pe.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         pe.actor_id AS actor_id
  FROM product_events pe
  JOIN declared d ON d.project = pe.project
  WHERE json_extract(pe.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  -- System dimensions roll up unconditionally, independent of the declared
  -- list, so these two arms do not join projects at all. The columns are
  -- NOT NULL DEFAULT '', so presence is <> '' rather than IS NOT NULL --
  -- matching the `present` predicate rollupProduct passes for them.
  SELECT project, substr(ts,1,10), event_name, '$platform', platform, actor_id
  FROM product_events WHERE platform <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM product_events WHERE app_version <> ''
),
counted AS (
  SELECT project, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals
  GROUP BY project, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project, day, event_name, attr_key, attr_value, c, u,
         ROW_NUMBER() OVER (PARTITION BY project, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
SELECT project, day, event_name, attr_key, attr_value, c, u
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project = v.project AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project, v.day, v.event_name, v.attr_key;
