# {params.project} — Groups

Groups work in both identity modes. `group_id` identifies an organization
rather than a natural person, so it is stored as given even when user
identifiers are salted.

<Dropdown name=range title="Date range" defaultValue="30">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql groups_daily
select day, count(distinct id) as active_groups
from twillingate.v_identity_daily
where project = '${params.project}' and kind = 'group' and id != ''
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by day order by day
```

```sql groups_totals
select count(distinct id) as groups, sum(hits + views + events) as actions
from twillingate.v_identity_daily
where project = '${params.project}' and kind = 'group' and id != ''
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql groups_top
select coalesce(i.name, d.id) as name,
       max(d.users) as users,
       sum(d.hits + d.views + d.events) as actions,
       count(distinct d.day) as active_days,
       max(d.day) as last_seen
from twillingate.v_identity_daily d
left join twillingate.identities i
  on i.project = d.project and i.kind = 'group' and i.id = d.id
where d.project = '${params.project}' and d.kind = 'group' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.id, i.name
order by actions desc limit 50
```

<Grid cols=2>
    <BigValue data={groups_totals} value=groups fmt=num0 title="Groups" />
    <BigValue data={groups_totals} value=actions fmt=num0 title="Actions" />
</Grid>

<LineChart data={groups_daily} x=day y=active_groups title="Active groups" yFmt=num0 />

## Most active groups

<Grid cols=2>
    <DataTable data={groups_top} rows=10 title="Activity" search=true>
        <Column id=name title="Group" />
        <Column id=actions title="Actions" fmt=num0 contentType=colorscale />
        <Column id=active_days title="Active days" fmt=num0 />
        <Column id=last_seen title="Last seen" />
    </DataTable>
    <BarChart data={groups_top} x=name y=users swapXY=true title="Users per group" yFmt=num0 />
</Grid>
