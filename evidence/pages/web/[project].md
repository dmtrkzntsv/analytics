# {params.project} — Web

```sql daily
select day, visitors, pageviews, sessions,
       case when sessions > 0 then bounces * 1.0 / sessions else 0 end as bounce_rate,
       case when sessions > 0 then duration_sec * 1.0 / sessions else 0 end as avg_session_sec
from analytics.v_web_daily
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
order by day
```

<BigValue data={daily} value=visitors sparkline=day comparison=pageviews />

<LineChart data={daily} x=day y={["visitors","pageviews"]} title="Visitors & Pageviews (90d)" />

<LineChart data={daily} x=day y=bounce_rate yFmt=pct title="Bounce rate" />

```sql pages
select path, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_pages
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by path order by pageviews desc limit 20
```

```sql referrers
select source, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_referrers
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d') and source != ''
group by source order by visitors desc limit 20
```

```sql countries
select country, sum(visitors) as visitors
from analytics.v_web_countries
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d') and country != ''
group by country order by visitors desc limit 20
```

```sql devices
select device, sum(visitors) as visitors from analytics.v_web_devices
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by device order by visitors desc
```

```sql browsers
select browser, sum(visitors) as visitors from analytics.v_web_browsers
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d') and browser != ''
group by browser order by visitors desc limit 10
```

```sql oses
select os, sum(visitors) as visitors from analytics.v_web_os
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d') and os != ''
group by os order by visitors desc limit 10
```

```sql campaigns
select utm_source, utm_medium, utm_campaign, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_utm
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by utm_source, utm_medium, utm_campaign order by visitors desc limit 20
```

## Top pages (30d)
<DataTable data={pages} rows=10 />

## Referrers (30d)
<DataTable data={referrers} rows=10 />

## Countries (30d)
<BarChart data={countries} x=country y=visitors swapXY=true />

## Devices / Browsers / OS (30d)
<BarChart data={devices} x=device y=visitors />
<BarChart data={browsers} x=browser y=visitors swapXY=true />
<BarChart data={oses} x=os y=visitors swapXY=true />

## Campaigns (30d)
<DataTable data={campaigns} rows=10 />
