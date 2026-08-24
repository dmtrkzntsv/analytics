-- Empty-database guard: see the note in v_web_daily.sql.
select project, day, browser, visitors, pageviews
from v_web_browsers
union all
select '', '1970-01-01', '', 0, 0
where not exists (select 1 from v_web_browsers)
