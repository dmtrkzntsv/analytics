# {params.project} — Retention

```sql retention_mode
select identity from analytics.projects where alias = '${params.project}'
```

{#if retention_mode[0].identity === 'identified'}

Web and app curves are kept apart: a browser visitor id and an app
`install_id` are different actors even for the same person, and blending
their curves describes neither population.

```sql retention_curve
select surface, day_offset,
       sum(actors) as actors, sum(cohort_size) as cohort_size,
       case when sum(cohort_size) > 0
            then sum(actors) * 1.0 / sum(cohort_size) else 0 end as retention
from analytics.v_retention
where project = '${params.project}' and day_offset <= 30
group by surface, day_offset
order by surface, day_offset
```

```sql retention_cohorts
select surface, cohort_day, day_offset, cohort_size, actors,
       case when cohort_size > 0 then actors * 1.0 / cohort_size else 0 end as retention
from analytics.v_retention
where project = '${params.project}' and day_offset <= 30
order by cohort_day desc, surface, day_offset
```

<LineChart data={retention_curve} x=day_offset y=retention series=surface yFmt=pct
  title="Retention by day offset" />

## Cohorts
<DataTable data={retention_cohorts} rows=30 />

{:else}

Retention is undefined in **anonymous** identity mode: `actor_id` rotates at
midnight, so every cohort would contain only its own first day. Set
`"identity": "identified"` in `projects.json` to enable cohorts.

{/if}
