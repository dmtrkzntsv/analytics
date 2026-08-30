-- The reserved key $app_version is now $version on the wire, and every
-- stored surface follows: the typed columns on product_events, app_views
-- and agg_app_versions become `version`, and the rows rollupProduct has
-- already written under attr_key='$app_version' are rewritten so a
-- breakdown query never sees the same dimension split across two keys.
--
-- Dependent views are dropped before the column renames rather than
-- letting ALTER TABLE rewrite them: v_events_flat is runtime-generated
-- (internal/app recreates it at boot from the declared-attributes
-- registry, so it is only dropped here), and the other two are recreated
-- below with the new column name. The v_product_attrs text must stay
-- numerically identical to rollupAttrValue in aggregate_product.go --
-- see 007, which this replaces verbatim except for the renamed column
-- and key.

DROP VIEW IF EXISTS v_events_flat;
DROP VIEW v_app_versions;
DROP VIEW v_product_attrs;

ALTER TABLE product_events RENAME COLUMN app_version TO version;
ALTER TABLE app_views RENAME COLUMN app_version TO version;
ALTER TABLE agg_app_versions RENAME COLUMN app_version TO version;

UPDATE agg_product_attrs SET attr_key = '$version' WHERE attr_key = '$app_version';

CREATE VIEW v_app_versions AS
SELECT project, day, platform, version, actives, views FROM agg_app_versions
UNION ALL
SELECT project, substr(ts,1,10), platform, version, COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), platform, version;

CREATE VIEW v_product_attrs AS
WITH cap AS (
  -- The cardinality cap, resolved exactly the way AggregateProductDay
  -- resolves it (aggregate_product.go): a non-positive topN clamps to
  -- defaultAttrsTopN rather than erroring, because `rn <= 0` keeps
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
  SELECT project, substr(ts,1,10), event_name, '$version', version, actor_id
  FROM product_events WHERE version <> ''
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
