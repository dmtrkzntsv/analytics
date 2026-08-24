package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// identityKinds are the two dimensions agg_identity_daily rolls up, paired
// with the raw column each reads.
var identityKinds = []struct{ kind, column string }{
	{"user", "user_id"},
	{"group", "group_id"},
}

// AggregateIdentityDay rolls one day's activity per user and per group, then
// refreshes identities.last_seen_day so display-name rows age out alongside
// their subjects.
//
// users counts distinct users active in a group that day; for kind='user' it
// is always 1, since the row already is one user.
//
// Must run before AggregateAppDay for the same day, which deletes the raw
// rows this reads.
func (d *DB) AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, k := range identityKinds {
			q := fmt.Sprintf(`
INSERT OR REPLACE INTO agg_identity_daily
	(project, day, kind, id, actors, users, hits, views, events)
WITH src AS (
  SELECT %[1]s AS id, actor_id, user_id, 1 AS is_hit, 0 AS is_view, 0 AS is_event
  FROM web_hits       WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 1, 0
  FROM app_views      WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 0, 1
  FROM product_events WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
),
ranked AS (
  SELECT id,
         COUNT(DISTINCT actor_id) AS actors,
         COUNT(DISTINCT NULLIF(user_id, '')) AS users,
         SUM(is_hit) AS hits, SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, id) AS rn
  FROM src GROUP BY id
)
SELECT ?, ?, ?, id, actors,
       CASE WHEN ? = 'user' THEN 1 ELSE users END,
       hits, views, events
FROM ranked WHERE rn <= %[2]d`, k.column, topNDimension)

			if _, err := tx.ExecContext(ctx, q,
				project, day.String(), project, day.String(), project, day.String(),
				project, day.String(), k.kind, k.kind); err != nil {
				return fmt.Errorf("agg_identity_daily %s: %w", k.kind, err)
			}

			if _, err := tx.ExecContext(ctx, `
UPDATE identities SET last_seen_day=?
WHERE project=? AND kind=? AND id IN (
  SELECT id FROM agg_identity_daily WHERE project=? AND day=? AND kind=?
)`, day.String(), project, k.kind, project, day.String(), k.kind); err != nil {
				return fmt.Errorf("identities last_seen %s: %w", k.kind, err)
			}
		}
		return nil
	})
}

// PruneIdentities drops display names and identity aggregates that have aged
// out, on the same window as actors. Names are PII, so letting them outlive
// the aggregates they label would be the wrong default.
func (d *DB) PruneIdentities(ctx context.Context, project string, before civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identities WHERE project=? AND last_seen_day <> '' AND last_seen_day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune identities: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agg_identity_daily WHERE project=? AND day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune agg_identity_daily: %w", err)
		}
		return nil
	})
}
