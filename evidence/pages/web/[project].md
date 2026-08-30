# {params.project} — Web

<Dropdown name=range title="Date range" defaultValue="7">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql daily
select day, visitors, pageviews, sessions,
       case when sessions > 0 then bounces * 1.0 / sessions else 0 end as bounce_rate,
       case when sessions > 0 then duration_sec * 1.0 / sessions else 0 end as avg_session_sec
from twillingate.v_web_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql totals
select sum(visitors) as visitors, sum(pageviews) as pageviews, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(bounces) * 1.0 / sum(sessions) else 0 end as bounce_rate,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_web_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=4>
    <BigValue data={totals} value=visitors fmt=num0 title="Visitors" />
    <BigValue data={totals} value=pageviews fmt=num0 title="Pageviews" />
    <BigValue data={totals} value=bounce_rate fmt=pct1 title="Bounce rate" />
    <BigValue data={totals} value=avg_session_sec fmt=num0 title="Avg session (sec)" />
</Grid>

<LineChart data={daily} x=day y={["visitors","pageviews"]} title="Visitors & pageviews" yFmt=num0 />

<Grid cols=2>
    <LineChart data={daily} x=day y=bounce_rate yFmt=pct1 title="Bounce rate" />
    <LineChart data={daily} x=day y=avg_session_sec yFmt=num0 title="Avg session length (sec)" />
</Grid>

```sql pages
select path, sum(visitors) as visitors, sum(pageviews) as pageviews,
       '/web/${params.project}/page?path=' || path as detail_url
from twillingate.v_web_pages
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by path order by pageviews desc limit 20
```

```sql referrers
select source, sum(visitors) as visitors, sum(pageviews) as pageviews
from twillingate.v_web_referrers
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and source != ''
group by source order by visitors desc limit 20
```

## Pages & referrers

<Grid cols=2>
    <DataTable data={pages} rows=10 title="Top pages">
        <Column id=detail_url title="Path" contentType=link linkLabel=path />
        <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
        <Column id=pageviews title="Views" fmt=num0 />
    </DataTable>
    <BarChart data={referrers} x=source y=visitors swapXY=true title="Referrers" yFmt=num0 />
</Grid>

```sql countries
select country, sum(visitors) as visitors
from twillingate.v_web_countries
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and country != ''
group by country order by visitors desc limit 20
```

```sql devices
select device, sum(visitors) as visitors from twillingate.v_web_devices
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and device != ''
group by device order by visitors desc
```

```sql browsers
select browser, sum(visitors) as visitors from twillingate.v_web_browsers
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and browser != ''
group by browser order by visitors desc limit 10
```

```sql oses
select os, sum(visitors) as visitors from twillingate.v_web_os
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and os != ''
group by os order by visitors desc limit 10
```

## Audience

<Grid cols=2>
    <BarChart data={countries} x=country y=visitors swapXY=true title="Countries" yFmt=num0 />
    <BarChart data={devices} x=device y=visitors title="Devices" yFmt=num0 />
</Grid>

<Grid cols=2>
    <BarChart data={browsers} x=browser y=visitors swapXY=true title="Browsers" yFmt=num0 />
    <BarChart data={oses} x=os y=visitors swapXY=true title="Operating systems" yFmt=num0 />
</Grid>

```sql campaigns
select utm_source, utm_medium, utm_campaign, sum(visitors) as visitors, sum(pageviews) as pageviews
from twillingate.v_web_utm
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and utm_source != ''
group by utm_source, utm_medium, utm_campaign order by visitors desc limit 20
```

## Campaigns

<DataTable data={campaigns} rows=10>
    <Column id=utm_source title="Source" />
    <Column id=utm_medium title="Medium" />
    <Column id=utm_campaign title="Campaign" />
    <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
    <Column id=pageviews title="Views" fmt=num0 />
</DataTable>
