-- Display names are opt-in, so this table is empty until a client sends
-- $user_name or $group_name. The sqlite connector infers column types from
-- the first row, so a zero-row result fails the source build and takes the
-- users and groups pages down with it. Emit a sentinel; the pages join on
-- project and never match it.
--
-- last_seen_day is deliberately not exposed: it is '' until the first daily
-- pass fills it, and a column mixing '' with dates breaks type inference the
-- same way. Pages derive last-seen from v_identity_daily instead.
select project, kind, id, name from identities
union all
select '', '', '', '' where not exists (select 1 from identities)
