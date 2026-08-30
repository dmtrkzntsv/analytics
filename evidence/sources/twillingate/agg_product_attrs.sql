-- The attribute-breakdown rollup job may not have run yet, leaving this table
-- empty. The sqlite connector infers column types from the first row, so a
-- zero-row result throws "Cannot convert undefined or null to object" and
-- fails the source build -- which breaks every query on the product page, not
-- just this one. Emit a sentinel row when the table is empty so the parquet
-- always has an inferable schema; pages filter it out on project = ''.
select project, day, event_name, attr_key, attr_value, count, unique_users
from agg_product_attrs
union all
select '', '1970-01-01', '', '', '', 0, 0
where not exists (select 1 from agg_product_attrs)
