# {params.project} — App

<Dropdown name=range title="Date range" defaultValue="30">
    <DropdownOption value="1" valueLabel="Last 1 day" />
    <DropdownOption value="7" valueLabel="Last 7 days" />
    <DropdownOption value="30" valueLabel="Last 30 days" />
    <DropdownOption value="90" valueLabel="Last 90 days" />
    <DropdownOption value="180" valueLabel="Last 180 days" />
</Dropdown>

```sql app_daily
select day, actives, views, sessions,
       case when sessions > 0 then duration_sec * 1.0 / sessions else 0 end as avg_session_sec,
       case when actives > 0 then sessions * 1.0 / actives else 0 end as sessions_per_active
from twillingate.v_app_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql app_totals
select sum(actives) as actives, sum(views) as views, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_app_daily
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=4>
    <BigValue data={app_totals} value=actives fmt=num0 title="Active installs (sum)" />
    <BigValue data={app_totals} value=views fmt=num0 title="Screen views" />
    <BigValue data={app_totals} value=sessions fmt=num0 title="Sessions" />
    <BigValue data={app_totals} value=avg_session_sec fmt=num0 title="Avg session (sec)" />
</Grid>

<LineChart data={app_daily} x=day y={["actives","views"]} title="Active installs & screen views" yFmt=num0 />

<Grid cols=2>
    <LineChart data={app_daily} x=day y=sessions title="Sessions" yFmt=num0 />
    <LineChart data={app_daily} x=day y=avg_session_sec yFmt=num0 title="Avg session length (sec)" />
</Grid>

```sql app_versions
select day, platform || ' ' || app_version as version, actives
from twillingate.v_app_versions
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and app_version != ''
order by day, version
```

## Version adoption

Watch a release roll out here, and find the version a metric fell off at.
Platform is part of the series because `2.4.1` means unrelated things on iOS
and Android.

<AreaChart data={app_versions} x=day y=actives series=version title="Active installs by version" yFmt=num0 />

```sql app_screens
select screen, sum(views) as views, sum(actives) as actives
from twillingate.v_app_screens
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and screen != ''
group by screen order by views desc limit 20
```

```sql app_platforms
select platform, sum(actives) as actives
from twillingate.v_app_versions
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and platform != ''
group by platform order by actives desc
```

## Screens & platform

<Grid cols=2>
    <DataTable data={app_screens} rows=10 title="Top screens">
        <Column id=screen title="Screen" />
        <Column id=views title="Views" fmt=num0 contentType=colorscale />
        <Column id=actives title="Installs" fmt=num0 />
    </DataTable>
    <BarChart data={app_platforms} x=platform y=actives title="Platform" yFmt=num0 />
</Grid>

```sql app_oses
select platform || ' ' || os_version as os, sum(actives) as actives
from twillingate.v_app_os
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and os_version != ''
group by os order by actives desc limit 15
```

```sql app_devices
select device_model, sum(actives) as actives
from twillingate.v_app_devices
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and device_model != ''
group by device_model order by actives desc limit 15
```

```sql app_countries
select country, sum(actives) as actives
from twillingate.v_app_countries
where project = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range.value} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and country != ''
group by country order by actives desc limit 20
```

## OS, devices & countries

<Grid cols=2>
    <BarChart data={app_oses} x=os y=actives swapXY=true title="OS version" yFmt=num0 />
    <BarChart data={app_devices} x=device_model y=actives swapXY=true title="Device model" yFmt=num0 />
</Grid>

<BarChart data={app_countries} x=country y=actives swapXY=true title="Countries" yFmt=num0 />
