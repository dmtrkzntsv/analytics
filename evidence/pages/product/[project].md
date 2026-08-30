# {params.project} — Product

<Dropdown name=range title="Date range" defaultValue="7">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql totals
select day, total_events, active_users
from twillingate.v_product_totals
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql headline
select sum(total_events) as total_events, max(active_users) as peak_dau,
       avg(active_users) as avg_dau
from twillingate.v_product_totals
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=3>
    <BigValue data={headline} value=total_events fmt=num0 title="Total events" />
    <BigValue data={headline} value=peak_dau fmt=num0 title="Peak DAU" />
    <BigValue data={headline} value=avg_dau fmt=num0 title="Avg DAU" />
</Grid>

<Grid cols=2>
    <LineChart data={totals} x=day y=active_users title="Daily active users" yFmt=num0 />
    <LineChart data={totals} x=day y=total_events title="Events per day" yFmt=num0 />
</Grid>

```sql events
select day, event_name, count, unique_users
from twillingate.v_product_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql event_summary
select event_name, sum(count) as total, max(unique_users) as peak_daily_uniques
from twillingate.v_product_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by event_name order by total desc
```

## Events

<Grid cols=2>
    <LineChart data={events} x=day y=count series=event_name title="Events by name" yFmt=num0 />
    <BarChart data={event_summary} x=event_name y=total swapXY=true title="Event volume" yFmt=num0 />
</Grid>

<DataTable data={event_summary} rows=10>
    <Column id=event_name title="Event" />
    <Column id=total title="Total" fmt=num0 contentType=colorscale />
    <Column id=peak_daily_uniques title="Peak daily uniques" fmt=num0 />
</DataTable>

```sql attr_breakdowns
select day, event_name, attr_key, attr_value, count, unique_users
from twillingate.agg_product_attrs
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

{#if attr_breakdowns.length > 0}

## Attribute breakdowns
<DataTable data={attr_breakdowns} rows=20 groupBy=attr_key />

{/if}
