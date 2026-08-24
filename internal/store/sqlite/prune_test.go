package sqlite

import (
	"context"
	"testing"
)

func TestPruneAggregates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Seed one old + one new row per representative table. agg_product_totals
	// is included because it is the only product table without event_name.
	exec(`INSERT INTO agg_web_daily VALUES ('app','2025-01-01',1,1,1,0,0), ('app','2026-08-01',2,2,2,0,0)`)
	exec(`INSERT INTO agg_web_pages VALUES ('app','2025-01-01','/',1,1), ('app','2026-08-01','/',2,2)`)
	exec(`INSERT INTO agg_web_utm VALUES ('app','2025-01-01','s','m','c',1,1), ('app','2026-08-01','s','m','c',2,2)`)
	exec(`INSERT INTO agg_product_daily VALUES ('app','2025-06-01','e',1,1), ('app','2026-08-01','e',2,2)`)
	exec(`INSERT INTO agg_product_totals VALUES ('app','2025-06-01',1,1), ('app','2026-08-01',2,2)`)
	exec(`INSERT INTO agg_product_attrs VALUES ('app','2025-06-01','e','k','v',1,1), ('app','2026-08-01','e','k','v',2,2)`)
	// Different project must be untouched.
	exec(`INSERT INTO agg_web_daily VALUES ('other','2025-01-01',9,9,9,0,0)`)

	if err := db.PruneAggregates(ctx, "app", day("2026-01-01"), day("2026-01-01"), day("2026-01-01")); err != nil {
		t.Fatal(err)
	}

	count := func(tbl, project string) int {
		t.Helper()
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE project=?`, project).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tbl := range []string{
		"agg_web_daily", "agg_web_pages", "agg_web_utm",
		"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	} {
		if n := count(tbl, "app"); n != 1 {
			t.Errorf("%s: %d rows for app, want 1 (old pruned, new kept)", tbl, n)
		}
	}
	if n := count("agg_web_daily", "other"); n != 1 {
		t.Error("other project must be untouched")
	}
}

// Web and product retention are configured independently, so a cutoff that
// prunes one must not prune the other.
func TestPruneAggregatesIndependentCutoffs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT INTO agg_web_daily VALUES ('app','2026-03-01',1,1,1,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO agg_product_daily VALUES ('app','2026-03-01','e',1,1)`); err != nil {
		t.Fatal(err)
	}
	// Prune web through 2026-06-01 but keep product back to 2026-01-01.
	if err := db.PruneAggregates(ctx, "app", day("2026-06-01"), day("2026-01-01"), day("2026-01-01")); err != nil {
		t.Fatal(err)
	}
	var web, product int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_web_daily`).Scan(&web); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_daily`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if web != 0 {
		t.Errorf("agg_web_daily: %d rows, want 0 (before web cutoff)", web)
	}
	if product != 1 {
		t.Errorf("agg_product_daily: %d rows, want 1 (after product cutoff)", product)
	}
}

// PruneAggregates must cover every agg_* table in the schema; a table added to
// a migration without being added to the prune lists would leak past retention.
func TestPruneAggregatesCoversAllAggTables(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'agg_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	pruned := map[string]bool{}
	all := append([]string{}, webAggTables...)
	all = append(all, productAggTables...)
	all = append(all, appAggTables...)
	all = append(all, identityAggTables...)
	// agg_retention is pruned by PruneActors alongside the actors rows it
	// derives from, not by PruneAggregates.
	all = append(all, "agg_retention")
	for _, tbl := range all {
		pruned[tbl] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !pruned[name] {
			t.Errorf("table %s exists but is not pruned by PruneAggregates", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalVacuumRuns(t *testing.T) {
	db := newTestDB(t)
	// The pragma is a silent no-op unless the database was created with
	// auto_vacuum=INCREMENTAL, so assert the mode alongside the call.
	var mode int
	if err := db.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Errorf("auto_vacuum=%d, want 2 (INCREMENTAL); incremental_vacuum would be a no-op", mode)
	}
	if err := db.IncrementalVacuum(context.Background()); err != nil {
		t.Fatal(err)
	}
}
