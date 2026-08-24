# {params.project} — Groups

Groups work in both identity modes: `group_id` identifies an organization
rather than a natural person, so it is stored as given even when user
identifiers are salted.

```sql groups_daily
select day, count(distinct id) as active_groups
from analytics.v_identity_daily
where project = '${params.project}' and kind = 'group'
  and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
group by day order by day
```

```sql groups_top
select d.id as group_id,
       coalesce(i.name, d.id) as name,
       max(d.users) as users,
       sum(d.hits) + sum(d.views) + sum(d.events) as activity,
       max(d.day) as last_seen
from analytics.v_identity_daily d
left join analytics.identities i
  on i.project = d.project and i.kind = 'group' and i.id = d.id
where d.project = '${params.project}' and d.kind = 'group'
  and d.day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by d.id, i.name
order by activity desc limit 50
```

<BigValue data={groups_daily} value=active_groups sparkline=day />

<LineChart data={groups_daily} x=day y=active_groups title="Active groups (90d)" />

## Most active groups (30d)
<DataTable data={groups_top} rows=25 />

<BarChart data={groups_top} x=name y=users swapXY=true title="Users per group" />
