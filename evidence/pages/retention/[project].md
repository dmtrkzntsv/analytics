# {params.project} — Retention

```sql retention_mode
select identity from twillingate.projects where alias = '${params.project}'
```

{#if retention_mode[0].identity === 'identified'}

Web and app curves are kept apart: a browser visitor id and an app
`install_id` are different actors even for the same person, and blending their
curves would describe neither population.

```sql retention_curve
select surface, day_offset,
       sum(actors) as actors, sum(cohort_size) as cohort_size,
       case when sum(cohort_size) > 0
            then sum(actors) * 1.0 / sum(cohort_size) else 0 end as retention
from twillingate.v_retention
where project = '${params.project}' and day_offset between 0 and 30
group by surface, day_offset
order by surface, day_offset
```

```sql retention_milestones
select surface,
       max(case when day_offset = 1 then retention end) as d1,
       max(case when day_offset = 7 then retention end) as d7,
       max(case when day_offset = 30 then retention end) as d30
from (
  select surface, day_offset,
         case when sum(cohort_size) > 0
              then sum(actors) * 1.0 / sum(cohort_size) else 0 end as retention
  from twillingate.v_retention
  where project = '${params.project}' and day_offset in (1, 7, 30)
  group by surface, day_offset
)
group by surface
```

```sql retention_cohorts
select surface, cohort_day, day_offset, cohort_size, actors,
       case when cohort_size > 0 then actors * 1.0 / cohort_size else 0 end as retention
from twillingate.v_retention
where project = '${params.project}' and day_offset between 0 and 30
order by cohort_day desc, surface, day_offset
```

<DataTable data={retention_milestones} rows=5 title="Milestones">
    <Column id=surface title="Surface" />
    <Column id=d1 title="D1" fmt=pct1 />
    <Column id=d7 title="D7" fmt=pct1 />
    <Column id=d30 title="D30" fmt=pct1 />
</DataTable>

<LineChart data={retention_curve} x=day_offset y=retention series=surface yFmt=pct1
  title="Retention by day offset" />

## Cohorts

<DataTable data={retention_cohorts} rows=20 search=true>
    <Column id=cohort_day title="Cohort" />
    <Column id=surface title="Surface" />
    <Column id=day_offset title="Day" fmt=num0 />
    <Column id=cohort_size title="Size" fmt=num0 />
    <Column id=actors title="Returned" fmt=num0 />
    <Column id=retention title="Retention" fmt=pct1 contentType=colorscale />
</DataTable>

{:else}

Retention is undefined in **anonymous** identity mode: `actor_id` rotates at
midnight, so every cohort would contain only its own first day.

Run `twillingate project update -alias {params.project} -identity identified`
(or the `update_project` MCP tool) to enable cohorts. Note that identified
mode stores a persistent `localStorage` id on the web, which is
terminal-equipment storage under ePrivacy — the same legal category as a
cookie.

{/if}
