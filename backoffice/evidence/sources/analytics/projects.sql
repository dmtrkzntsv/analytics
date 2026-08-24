-- Empty-database guard: see the note in v_web_daily.sql. A brand-new install
-- has no projects row until the first config sync, and a zero-row result here
-- fails the source build. index.md filters the sentinel out on alias != ''.
select alias, name, archived_at from projects
union all
select '', '', null where not exists (select 1 from projects)
