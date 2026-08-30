-- product_aggregation collapses to a flat declared key list. `enabled` is
-- dropped (rollups are now unconditional) and `top_n` moves to the global
-- PRODUCT_ATTRIBUTES_TOP_N setting. The backfill takes the DISTINCT union
-- of every array in the old event-keyed map, so
--   {"*":["plan"],"subscribed":["tier","plan"]}  ->  ["plan","tier"]
ALTER TABLE projects ADD COLUMN attributes TEXT NOT NULL DEFAULT '[]';

UPDATE projects SET attributes = COALESCE((
  SELECT json_group_array(k) FROM (
    SELECT DISTINCT v.value AS k
    FROM json_each(json_extract(projects.product_aggregation, '$.attributes')) AS m,
         json_each(m.value) AS v
    ORDER BY 1
  )
), '[]')
WHERE product_aggregation IS NOT NULL
  AND product_aggregation <> ''
  AND json_extract(product_aggregation, '$.attributes') IS NOT NULL;

ALTER TABLE projects DROP COLUMN product_aggregation;
