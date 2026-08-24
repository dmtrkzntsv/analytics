# {$page.url.searchParams.get('path')}

[← back to {params.project}](/web/{params.project})

<Dropdown name=range title="Date range" defaultValue="30">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

<!--
  The path comes from the query string, and Evidence interpolates ${...} into
  the SQL text verbatim -- an unescaped value can close the string literal and
  rewrite the predicate. Doubling single quotes keeps it a literal.
-->

```sql page_daily
select day, visitors, pageviews
from analytics.v_web_pages
where project = '${params.project}'
  and path = '${($page.url.searchParams.get('path') ?? '').replaceAll("'", "''")}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql page_totals
select sum(visitors) as visitors, sum(pageviews) as pageviews,
       case when sum(visitors) > 0 then sum(pageviews) * 1.0 / sum(visitors) else 0 end as views_per_visitor
from analytics.v_web_pages
where project = '${params.project}'
  and path = '${($page.url.searchParams.get('path') ?? '').replaceAll("'", "''")}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=3>
    <BigValue data={page_totals} value=visitors fmt=num0 title="Visitors" />
    <BigValue data={page_totals} value=pageviews fmt=num0 title="Pageviews" />
    <BigValue data={page_totals} value=views_per_visitor fmt=num1 title="Views per visitor" />
</Grid>

<LineChart data={page_daily} x=day y={["visitors","pageviews"]} title="Traffic to this page" yFmt=num0 />

<DataTable data={page_daily} rows=15>
    <Column id=day title="Day" />
    <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
    <Column id=pageviews title="Pageviews" fmt=num0 />
</DataTable>
