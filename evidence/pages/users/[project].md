# {params.project} — Users

```sql users_mode
select identity from twillingate.projects where alias = '${params.project}'
```

{#if users_mode[0].identity === 'identified'}

<Dropdown name=range title="Date range" defaultValue="30">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql users_daily
select day, count(distinct id) as active_users, sum(hits + views + events) as actions
from twillingate.v_identity_daily
where project = '${params.project}' and kind = 'user' and id != ''
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by day order by day
```

```sql users_totals
select count(distinct id) as users, sum(hits + views + events) as actions,
       case when count(distinct id) > 0
            then sum(hits + views + events) * 1.0 / count(distinct id) else 0 end as per_user
from twillingate.v_identity_daily
where project = '${params.project}' and kind = 'user' and id != ''
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql users_top
select coalesce(i.name, d.id) as name,
       sum(d.hits + d.views + d.events) as actions,
       count(distinct d.day) as active_days,
       max(d.day) as last_seen
from twillingate.v_identity_daily d
left join twillingate.identities i
  on i.project = d.project and i.kind = 'user' and i.id = d.id
where d.project = '${params.project}' and d.kind = 'user' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.id, i.name
order by actions desc limit 50
```

<Grid cols=3>
    <BigValue data={users_totals} value=users fmt=num0 title="Users" />
    <BigValue data={users_totals} value=actions fmt=num0 title="Actions" />
    <BigValue data={users_totals} value=per_user fmt=num1 title="Actions per user" />
</Grid>

<LineChart data={users_daily} x=day y=active_users title="Active users" yFmt=num0 />

## Most active users

<DataTable data={users_top} rows=15 search=true>
    <Column id=name title="User" />
    <Column id=actions title="Actions" fmt=num0 contentType=colorscale />
    <Column id=active_days title="Active days" fmt=num0 />
    <Column id=last_seen title="Last seen" />
</DataTable>

{:else}

This project runs in **anonymous** identity mode: `user_id` is a hash that
rotates at midnight, so a per-user report would be a list of hashes that means
nothing tomorrow.

Run `twillingate project update -alias {params.project} -identity identified`
(or the `update_project` MCP tool) to enable per-user reporting. Note that
identified mode stores a persistent `localStorage` id on the web, which is
terminal-equipment storage under ePrivacy — the same legal category as a
cookie.

Group reporting works in both modes: see [Groups](/groups/{params.project}).

{/if}
