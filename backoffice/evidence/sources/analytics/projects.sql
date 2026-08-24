-- Empty-database guard: see the note in v_web_daily.sql. A brand-new install
-- has no projects row until the first config sync, and a zero-row result here
-- fails the source build. index.md filters the sentinel out on alias != ''.
--
-- identity drives the users and retention pages, which are only meaningful
-- for identified projects; the sentinel carries the default so those pages
-- render their explanatory branch rather than erroring on an empty database.
select alias, name, identity, archived_at from projects
union all
select '', '', 'anonymous', null where not exists (select 1 from projects)
