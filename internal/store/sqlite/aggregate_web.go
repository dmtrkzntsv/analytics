package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// sessionsCTE computes per-session rows for one project+day. A session is
// a per-visitor run of hits with gaps <= 30 min (spec §9).
const sessionsCTE = `
WITH hits AS (
  SELECT actor_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM web_hits WHERE project = :p AND ts >= :from AND ts < :to
),
marked AS (
  SELECT actor_id, t,
         CASE WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1 ELSE 0 END AS new_session
  FROM hits WINDOW w AS (PARTITION BY actor_id ORDER BY t)
),
numbered AS (
  SELECT actor_id, t,
         SUM(new_session) OVER (PARTITION BY actor_id ORDER BY t) AS session_no
  FROM marked
),
sessions AS (
  SELECT actor_id, session_no, COUNT(*) AS hit_count, MAX(t) - MIN(t) AS duration
  FROM numbered GROUP BY actor_id, session_no
)`

func dayRange(day civil.Date) (string, string) {
	return day.String() + "T00:00:00Z", day.AddDays(1).String() + "T00:00:00Z"
}

func (d *DB) WebDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "web_hits", project, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "product_events", project, before)
}

func (d *DB) daysBefore(ctx context.Context, table, project string, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT DISTINCT substr(ts,1,10) FROM %s WHERE project=? AND ts < ? ORDER BY 1`, table),
		project, before.String()+"T00:00:00Z")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []civil.Date
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		day, err := civil.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

func (d *DB) AggregateWebDay(ctx context.Context, project string, day civil.Date) error {
	from, to := dayRange(day)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM web_hits WHERE project=? AND ts>=? AND ts<?`,
			project, from, to).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil // already aggregated (or empty day): no-op keeps idempotency
		}
		named := []any{sql.Named("p", project), sql.Named("from", from), sql.Named("to", to), sql.Named("day", day.String())}
		if _, err := tx.ExecContext(ctx, sessionsCTE+`
			INSERT OR REPLACE INTO agg_web_daily
			  (project, day, visitors, pageviews, sessions, bounces, duration_sec)
			SELECT :p, :day,
			  (SELECT COUNT(DISTINCT actor_id) FROM hits),
			  (SELECT COUNT(*) FROM hits),
			  COUNT(*),
			  SUM(CASE WHEN hit_count = 1 THEN 1 ELSE 0 END),
			  COALESCE(SUM(duration), 0)
			FROM sessions`, named...); err != nil {
			return fmt.Errorf("agg_web_daily: %w", err)
		}
		type dim struct{ table, cols, group, where string }
		dims := []dim{
			{"agg_web_pages", "path", "path", ""},
			{"agg_web_referrers", "source", "referrer_source", ""},
			{"agg_web_countries", "country", "country", ""},
			{"agg_web_devices", "device", "device", ""},
			{"agg_web_browsers", "browser", "browser", ""},
			{"agg_web_os", "os", "os", ""},
			{"agg_web_utm", "utm_source, utm_medium, utm_campaign", "utm_source, utm_medium, utm_campaign",
				"AND NOT (utm_source='' AND utm_medium='' AND utm_campaign='')"},
		}
		for _, dm := range dims {
			q := fmt.Sprintf(`INSERT OR REPLACE INTO %s (project, day, %s, visitors, pageviews)
				SELECT :p, :day, %s, COUNT(DISTINCT actor_id), COUNT(*)
				FROM web_hits
				WHERE project = :p AND ts >= :from AND ts < :to %s
				GROUP BY %s`, dm.table, dm.cols, dm.group, dm.where, dm.group)
			if _, err := tx.ExecContext(ctx, q, named...); err != nil {
				return fmt.Errorf("%s: %w", dm.table, err)
			}
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM web_hits WHERE project=? AND ts>=? AND ts<?`, project, from, to)
		return err
	})
}
