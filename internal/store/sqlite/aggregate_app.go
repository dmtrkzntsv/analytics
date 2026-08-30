package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// topNDimension caps client-supplied free-string dimensions per day; the
// tail collapses into "(other)". Web's path stays uncapped (existing
// behaviour, unchanged here), but an unbounded *new* dimension is the wrong
// default on the SD-card hardware target: screen names carrying record ids
// would grow the aggregate tables without limit.
const topNDimension = 500

const otherBucket = "(other)"

// AppDaysBefore lists days with raw app_views strictly before the cutoff.
func (d *DB) AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "app_views", project, before)
}

// AggregateAppDay rolls one day of app_views into the agg_app_* family and
// deletes that day's raw rows, in one transaction.
//
// Idempotent by construction: every write is INSERT OR REPLACE keyed on
// (project, day, ...) and recomputed wholly from the raw rows the same
// transaction then removes.
func (d *DB) AggregateAppDay(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		// Sessions: a client-declared session_id is authoritative because
		// the app knows its own foreground/background transitions. Absent
		// one, a gap of more than 30 minutes over actor_id splits sessions,
		// matching the web sessionizer.
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO agg_app_daily (project, day, actives, views, sessions, duration_sec)
WITH src AS (
  SELECT actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM app_views WHERE project=? AND substr(ts,1,10)=?
),
marked AS (
  SELECT actor_id, session_id, t,
         CASE WHEN session_id <> '' THEN 0
              WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
              ELSE 0 END AS new_session
  FROM src WINDOW w AS (PARTITION BY actor_id ORDER BY t)
),
keyed AS (
  SELECT actor_id, t,
         CASE WHEN session_id <> '' THEN session_id
              ELSE CAST(SUM(new_session) OVER (PARTITION BY actor_id ORDER BY t) AS TEXT)
         END AS skey
  FROM marked
),
spans AS (
  SELECT actor_id, skey, MAX(t) - MIN(t) AS dur FROM keyed GROUP BY actor_id, skey
)
SELECT ?, ?,
       (SELECT COUNT(DISTINCT actor_id) FROM src),
       (SELECT COUNT(*) FROM src),
       (SELECT COUNT(*) FROM spans),
       (SELECT COALESCE(SUM(dur), 0) FROM spans)
WHERE EXISTS (SELECT 1 FROM src)`,
			project, day.String(), project, day.String()); err != nil {
			return fmt.Errorf("agg_app_daily: %w", err)
		}

		for _, dim := range appDimensions {
			if err := aggAppDimension(ctx, tx, dim, project, day); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM app_views WHERE project=? AND substr(ts,1,10)=?`,
			project, day.String()); err != nil {
			return fmt.Errorf("prune raw app_views: %w", err)
		}
		return nil
	})
}

// appDimension is one rollup: keys are the dimension columns, and the last
// of them is the one whose tail collapses into "(other)". For two-column
// dimensions the leading column stays intact, so a collapsed version row
// still says which platform it belongs to.
type appDimension struct {
	table string
	keys  []string
}

var appDimensions = []appDimension{
	{"agg_app_screens", []string{"screen"}},
	{"agg_app_versions", []string{"platform", "version"}},
	{"agg_app_os", []string{"platform", "os_version"}},
	{"agg_app_devices", []string{"device_model"}},
	{"agg_app_countries", []string{"country"}},
}

// aggAppDimension rolls one dimension in a single statement, mapping every
// value outside the top N by views onto "(other)".
//
// Doing it in one statement rather than a top-N insert plus a tail insert
// keeps actives honest: it is always COUNT(DISTINCT actor_id) over the
// grouped raw rows, so the collapsed bucket counts installs rather than
// summing per-value counts and double-counting anyone who saw two of the
// collapsed screens.
func aggAppDimension(ctx context.Context, tx *sql.Tx, dim appDimension, project string, day civil.Date) error {
	cols := strings.Join(dim.keys, ", ")
	last := dim.keys[len(dim.keys)-1]

	var lead, join strings.Builder
	for _, k := range dim.keys[:len(dim.keys)-1] {
		fmt.Fprintf(&lead, "v.%s, ", k)
	}
	for i, k := range dim.keys {
		if i > 0 {
			join.WriteString(" AND ")
		}
		fmt.Fprintf(&join, "r.%s = v.%s", k, k)
	}
	bucket := fmt.Sprintf("CASE WHEN r.rn <= %d THEN v.%s ELSE '%s' END",
		topNDimension, last, otherBucket)

	q := fmt.Sprintf(`
INSERT OR REPLACE INTO %[1]s (project, day, %[2]s, actives, views)
SELECT ?, ?, %[3]s%[4]s, COUNT(DISTINCT v.actor_id), COUNT(*)
FROM app_views v
JOIN (
  SELECT %[2]s, ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, %[2]s) AS rn
  FROM app_views WHERE project=? AND substr(ts,1,10)=? GROUP BY %[2]s
) r ON %[5]s
WHERE v.project=? AND substr(v.ts,1,10)=?
GROUP BY %[3]s%[4]s`,
		dim.table, cols, lead.String(), bucket, join.String())

	if _, err := tx.ExecContext(ctx, q,
		project, day.String(), project, day.String(), project, day.String()); err != nil {
		return fmt.Errorf("%s: %w", dim.table, err)
	}
	return nil
}
