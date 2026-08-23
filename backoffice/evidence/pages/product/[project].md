# {params.project} — Product

```sql totals
select day, total_events, active_users
from analytics.v_product_totals
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
order by day
```

<BigValue data={totals} value=active_users sparkline=day title="DAU" />

<LineChart data={totals} x=day y=active_users title="Daily active users (90d)" />
<LineChart data={totals} x=day y=total_events title="Events per day (90d)" />

```sql events
select day, event_name, count, unique_users
from analytics.v_product_daily
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
order by day
```

<LineChart data={events} x=day y=count series=event_name title="Events by name" />

```sql event_summary
select event_name, sum(count) as total, max(unique_users) as peak_daily_uniques
from analytics.v_product_daily
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by event_name order by total desc
```

## Events (30d)
<DataTable data={event_summary} />

```sql attr_breakdowns
select day, event_name, attr_key, attr_value, count, unique_users
from analytics.agg_product_attrs
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
order by day
```

{#if attr_breakdowns.length > 0}

## Attribute breakdowns
<DataTable data={attr_breakdowns} rows=20 groupBy=attr_key />

{/if}
