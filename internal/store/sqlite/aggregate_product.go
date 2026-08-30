package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// attrPath builds a JSON path literal for a config-supplied attribute key.
// Keys come from the operator's config, not from clients, but are still
// quoted defensively (spec §8.1 sanitization rules apply to view building;
// here the value is parameter-adjacent SQL, so escape quotes).
func attrPath(key string) string {
	return `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
}

// defaultAttrsTopN is the fallback for a non-positive topN. Not reachable
// today (jobs.Runner always sets it from config.Config.ProductAttributesTopN,
// which parse() defaults to 50), but guarded anyway: rollupAttr's
// `rn <= topN` filter treats topN<=0 as "keep nothing", which would
// silently collapse every distinct value into "(other)" rather than erroring
// -- a permanent, undetected loss of attribute breakdowns.
const defaultAttrsTopN = 50

func (d *DB) AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error {
	if topN <= 0 {
		topN = defaultAttrsTopN
	}
	from, to := dayRange(day)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM product_events WHERE project=? AND ts>=? AND ts<?`,
			project, from, to).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := d.rollupProduct(ctx, tx, project, day, from, to, attrs, topN); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM product_events WHERE project=? AND ts>=? AND ts<?`, project, from, to)
		return err
	})
}

func (d *DB) rollupProduct(ctx context.Context, tx *sql.Tx, project string, day civil.Date, from, to string, attrs []string, topN int) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_daily
		(project, day, event_name, count, unique_users)
		SELECT project, ?, event_name, COUNT(*), COUNT(DISTINCT actor_id)
		FROM product_events WHERE project=? AND ts>=? AND ts<?
		GROUP BY event_name`, day.String(), project, from, to); err != nil {
		return fmt.Errorf("agg_product_daily: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_totals
		(project, day, total_events, active_users)
		SELECT project, ?, COUNT(*), COUNT(DISTINCT actor_id)
		FROM product_events WHERE project=? AND ts>=? AND ts<?
		GROUP BY project`, day.String(), project, from, to); err != nil {
		return fmt.Errorf("agg_product_totals: %w", err)
	}
	// Attribute breakdowns: resolve per event name present that day.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name FROM product_events
		WHERE project=? AND ts>=? AND ts<?`, project, from, to)
	if err != nil {
		return err
	}
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		for _, key := range attrs {
			path := attrPath(key)
			expr := `json_extract(attributes, :path)`
			present := expr + ` IS NOT NULL`
			named := []any{
				sql.Named("p", project), sql.Named("day", day.String()),
				sql.Named("from", from), sql.Named("to", to),
				sql.Named("event", event), sql.Named("key", key),
				sql.Named("path", path), sql.Named("n", topN),
			}
			if err := d.rollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event, key, err)
			}
		}
	}
	// System dimensions: platform and app_version are typed columns
	// written on every event, not declared custom keys, so they roll up
	// unconditionally under $-prefixed attr_keys. $ is a safe namespace:
	// resolveAttributes routes every $-prefixed input to a typed field
	// and drops unrecognised ones, so a custom key can never collide
	// with a system one. The columns are NOT NULL DEFAULT '', so empty
	// string (not NULL) means absent.
	for _, dim := range systemDims {
		for _, event := range events {
			named := []any{
				sql.Named("p", project), sql.Named("day", day.String()),
				sql.Named("from", from), sql.Named("to", to),
				sql.Named("event", event), sql.Named("key", dim.key),
				sql.Named("n", topN),
			}
			expr := dim.column
			present := dim.column + ` <> ''`
			if err := d.rollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("system dim %s/%s: %w", event, dim.key, err)
			}
		}
	}
	return nil
}

// systemDims maps a product_events column to the attr_key it rolls up
// under. The $ prefix is safe as a namespace because resolveAttributes
// routes every $-prefixed input to a typed field, so a custom key can
// never collide with one of these.
var systemDims = []struct{ column, key string }{
	{"platform", "$platform"},
	{"app_version", "$app_version"},
}

// rollupAttrValue writes the ranked top-N breakdown plus the "(other)"
// tail for one (event, attr_key) pair into agg_product_attrs. expr is the
// SQL expression yielding the value to group by (a json_extract path for
// declared attributes, a bare column for system dimensions); present is
// the filter identifying rows where that value counts as set (declared
// attributes use IS NOT NULL on the JSON extract, system columns use
// <> '' since they're NOT NULL DEFAULT ''). named must supply :p, :day,
// :from, :to, :event, :key, :n, and whatever expr/present reference
// (:path for the JSON case).
func (d *DB) rollupAttrValue(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	// Top-N values by count.
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
		  FROM product_events
		  WHERE project=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, c, u, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project, day, event_name, attr_key, attr_value, count, unique_users)
		SELECT :p, :day, :event, :key, v, c, u FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	// Tail -> "(other)" with correct distinct users, computed from raw.
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c
		  FROM product_events
		  WHERE project=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project, day, event_name, attr_key, attr_value, count, unique_users)
		SELECT :p, :day, :event, :key, '(other)', COUNT(*), COUNT(DISTINCT actor_id)
		FROM product_events
		WHERE project=:p AND ts>=:from AND ts<:to AND event_name=:event
		  AND `+present+`
		  AND `+expr+` NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}
