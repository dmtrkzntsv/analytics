-- Empty-database guard: the sqlite connector infers column types from the
-- first row, so a zero-row result throws "Cannot convert undefined or null to
-- object" and fails the whole source build -- taking every other query on the
-- page down with it. A fresh install has no traffic yet, so emit a sentinel
-- row when the view is empty; pages filter it out via their project clause.
select project, day, visitors, pageviews, sessions, bounces, duration_sec
from v_web_daily
union all
select '', '1970-01-01', 0, 0, 0, 0, 0
where not exists (select 1 from v_web_daily)
