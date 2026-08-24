package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// flatViewBaseColumns are the non-attribute columns of v_events_flat. Every
// attribute column carries an attr_ prefix, so none can collide with these.
var flatViewBaseColumns = []string{"id", "project", "event_name", "actor_id", "ts"}

// KnownAttributeKeys returns every distinct attribute key present in the raw
// product_events. attributes is NOT NULL DEFAULT '{}', so json_each is safe
// even for events recorded without attributes.
func (d *DB) KnownAttributeKeys(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT je.key FROM product_events, json_each(product_events.attributes) je`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// sanitizeAlias strips everything outside [A-Za-z0-9_] from an attribute key.
// The result is always safe to splice into DDL unquoted once prefixed, which
// is what keeps hostile keys from injecting SQL.
func sanitizeAlias(key string) string {
	var b strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RebuildFlatView replaces v_events_flat with one column per supplied
// attribute key, so BI tools see a plain wide table instead of JSON.
//
// Attribute keys are user-supplied, so the alias and the JSON path are
// handled separately: the alias is sanitized down to a safe identifier, while
// the path keeps the ORIGINAL key (quotes escaped) so lookups still match.
// Keys that sanitize to nothing are skipped; keys that sanitize to the same
// alias get _2, _3 ... suffixes.
func (d *DB) RebuildFlatView(ctx context.Context, keys []string) error {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted) // deterministic column order across rebuilds
	cols := append([]string(nil), flatViewBaseColumns...)
	used := map[string]bool{}
	for _, key := range sorted {
		alias := sanitizeAlias(key)
		if alias == "" {
			continue
		}
		alias = "attr_" + alias
		if used[alias] {
			base := alias
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("%s_%d", base, i)
				if !used[candidate] {
					alias = candidate
					break
				}
			}
		}
		used[alias] = true
		// The original key sits inside a JSON path literal, where only " is
		// special; the whole path then sits inside a SQL string literal,
		// where ' must be doubled.
		path := `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		pathLit := strings.ReplaceAll(path, `'`, `''`)
		cols = append(cols, fmt.Sprintf(`json_extract(attributes, '%s') AS %s`, pathLit, alias))
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS v_events_flat`); err != nil {
			return fmt.Errorf("drop v_events_flat: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`CREATE VIEW v_events_flat AS SELECT %s FROM product_events`,
			strings.Join(cols, ", "))); err != nil {
			return fmt.Errorf("create v_events_flat: %w", err)
		}
		return nil
	})
}
