# {params.project} — App

```sql app_daily
select day, actives, views, sessions,
       case when sessions > 0 then duration_sec * 1.0 / sessions else 0 end as avg_session_sec
from analytics.v_app_daily
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
order by day
```

<BigValue data={app_daily} value=actives sparkline=day comparison=views />

<LineChart data={app_daily} x=day y={["actives","views"]} title="Active installs & screen views (90d)" />

<LineChart data={app_daily} x=day y=sessions title="Sessions" />
<LineChart data={app_daily} x=day y=avg_session_sec title="Average session length (s)" />

```sql app_versions
select day, platform || ' ' || app_version as version, actives
from analytics.v_app_versions
where project = '${params.project}' and day >= strftime(current_date - interval 90 day, '%Y-%m-%d')
  and app_version != ''
order by day, version
```

## Version adoption (90d)

This is the chart to watch a release roll out on, and the one that shows
which version a metric fell off at. Platform is part of the series because
`2.4.1` means unrelated things on iOS and Android.

<AreaChart data={app_versions} x=day y=actives series=version title="Active installs by version" />

```sql app_screens
select screen, sum(views) as views, sum(actives) as actives
from analytics.v_app_screens
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
group by screen order by views desc limit 20
```

```sql app_platforms
select platform, sum(actives) as actives, sum(views) as views
from analytics.v_app_versions
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
  and platform != ''
group by platform order by actives desc
```

```sql app_oses
select platform || ' ' || os_version as os, sum(actives) as actives
from analytics.v_app_os
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
  and os_version != ''
group by os order by actives desc limit 15
```

```sql app_devices
select device_model, sum(actives) as actives
from analytics.v_app_devices
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
  and device_model != ''
group by device_model order by actives desc limit 15
```

```sql app_countries
select country, sum(actives) as actives
from analytics.v_app_countries
where project = '${params.project}' and day >= strftime(current_date - interval 30 day, '%Y-%m-%d')
  and country != ''
group by country order by actives desc limit 20
```

## Top screens (30d)
<DataTable data={app_screens} rows=10 />

## Platform / OS / Device (30d)
<BarChart data={app_platforms} x=platform y=actives />
<BarChart data={app_oses} x=os y=actives swapXY=true />
<BarChart data={app_devices} x=device_model y=actives swapXY=true />

## Countries (30d)
<BarChart data={app_countries} x=country y=actives swapXY=true />
