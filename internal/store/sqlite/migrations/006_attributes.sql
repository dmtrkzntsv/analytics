-- product_aggregation collapses to a flat declared key list. `enabled` is
-- dropped (rollups are now unconditional) and `top_n` moves to the global
-- PRODUCT_ATTRIBUTES_TOP_N setting. The backfill takes the DISTINCT union
-- of every array in the old event-keyed map, so
--   {"*":["plan"],"subscribed":["tier","plan"]}  ->  ["plan","tier"]
ALTER TABLE projects ADD COLUMN attributes TEXT NOT NULL DEFAULT '[]';

-- Migrations are forward-only with no scripted way back, so this must never
-- abort the transaction on a hand-edited or otherwise malformed
-- product_aggregation value — a row we can't safely interpret backfills to
-- '[]' instead of taking the whole server down. json_valid() rejects
-- non-JSON text outright; json_type(product_aggregation, '$.attributes')
-- confirms the path is an object *before* anything iterates it (json_type
-- reads the original column text, so it never chokes on an
-- already-dequoted scalar the way re-parsing json_each's own output
-- would); and m.type/v.type reuse json_each's own precomputed type column
-- rather than re-validating a dequoted value, which is what would
-- otherwise still raise "malformed JSON" on e.g.
-- {"attributes":{"*":"plan"}} (a scalar where an array is expected).
UPDATE projects SET attributes = COALESCE((
  SELECT json_group_array(k) FROM (
    SELECT DISTINCT v.value AS k
    FROM json_each(json_extract(projects.product_aggregation, '$.attributes')) AS m,
         json_each(m.value) AS v
    WHERE m.type = 'array' AND v.type = 'text'
    ORDER BY 1
  )
), '[]')
WHERE product_aggregation IS NOT NULL
  AND product_aggregation <> ''
  AND json_valid(product_aggregation)
  AND json_type(product_aggregation, '$.attributes') = 'object';

ALTER TABLE projects DROP COLUMN product_aggregation;
