# {params.project} — Users

```sql users_mode
select identity from analytics.projects where alias = '${params.project}'
```

{#if users_mode[0].identity === 'identified'}

```sql users_daily
select day, count(distinct id) as active_users
from analytics.v_identity_daily
where project = '${params.project}' and kind = 'user'
  and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
group by day order by day
```

```sql users_top
select d.id as user_id,
       coalesce(i.name, d.id) as name,
       sum(d.hits) + sum(d.views) + sum(d.events) as activity,
       max(d.day) as last_seen
from analytics.v_identity_daily d
left join analytics.identities i
  on i.project = d.project and i.kind = 'user' and i.id = d.id
where d.project = '${params.project}' and d.kind = 'user'
  and d.day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by d.id, i.name
order by activity desc limit 50
```

<BigValue data={users_daily} value=active_users sparkline=day />

<LineChart data={users_daily} x=day y=active_users title="Active users (90d)" />

## Most active users (30d)
<DataTable data={users_top} rows=25 />

{:else}

This project runs in **anonymous** identity mode: `user_id` is a hash that
rotates at midnight, so a per-user report would be a list of hashes that
means nothing tomorrow. Set `"identity": "identified"` in `projects.json` to
enable per-user reporting.

Group reporting works in both modes — see [Groups](/groups/{params.project}).

{/if}
