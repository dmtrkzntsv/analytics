package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// flatViewBaseColumns are the non-attribute columns of v_events_flat.
// attributes carries the raw JSON, so a key that isn't declared (and so
// gets no attr_ column) stays reachable via json_extract — the view is
// never a downgrade from the base table. Every attribute column carries an
// attr_ prefix, so none can collide with these.
var flatViewBaseColumns = []string{"id", "project", "event_name", "actor_id", "ts", "attributes"}

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
// attribute key (the registry's declared attributes, spec §3), so BI
// tools see a plain wide table instead of JSON.
//
// Attribute keys are operator-declared config now rather than raw
// client-supplied data, but the alias and the JSON path are still handled
// separately as defence in depth: the alias is sanitized down to a safe
// identifier, while the path keeps the ORIGINAL key (quotes escaped) so
// lookups still match. Keys that sanitize to nothing are skipped; keys
// that sanitize to the same alias get _2, _3 ... suffixes.
//
// A rebuild whose CREATE VIEW text matches the view already in place is a
// no-op: the daily pass calls this unconditionally as a repair net, and in
// steady state (no config change since last night) it should not pay for
// a DROP/CREATE. The comparison is against the exact statement text (built
// once, used for both the check and the ExecContext below) rather than
// just the column names: sanitizeAlias is lossy and many-to-one, so two
// declared-key sets that differ only in a hostile/unsanitized key (e.g.
// "plan!" -> "plan") can produce the SAME column names while needing
// different json_extract paths. Comparing names alone would falsely treat
// that as unchanged and leave the view extracting the stale key forever.
func (d *DB) RebuildFlatView(ctx context.Context, keys []string) error {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted) // deterministic column order across rebuilds
	exprs := append([]string(nil), flatViewBaseColumns...)
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
		exprs = append(exprs, fmt.Sprintf(`json_extract(attributes, '%s') AS %s`, pathLit, alias))
	}
	stmt := fmt.Sprintf(`CREATE VIEW v_events_flat AS SELECT %s FROM product_events`,
		strings.Join(exprs, ", "))

	if current, err := d.flatViewDefinition(ctx); err == nil && current == stmt {
		return nil
	}

	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS v_events_flat`); err != nil {
			return fmt.Errorf("drop v_events_flat: %w", err)
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create v_events_flat: %w", err)
		}
		return nil
	})
}

// flatViewDefinition reads the CREATE VIEW text sqlite_master has recorded
// for v_events_flat ("", nil if the view doesn't exist yet — a missing
// view is never a match), so RebuildFlatView can skip the DROP/CREATE only
// when the statement it would run is byte-identical to the one already in
// place. SQLite stores the statement verbatim as given, which is what
// makes this comparison exact rather than a column-name approximation.
func (d *DB) flatViewDefinition(ctx context.Context) (string, error) {
	var def string
	err := d.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'view' AND name = 'v_events_flat'`).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return def, nil
}
