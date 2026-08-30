package sqlite

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newTestDBAt applies migrations only up to and including version upTo,
// recording them in schema_migrations exactly as Migrate would, so a test
// can seed data in an old schema and then let Migrate apply the rest —
// the upgrade path a live deployment takes, which the usual
// fresh-DB-runs-everything tests never exercise against data.
func newTestDBAt(t *testing.T, upTo int) *DB {
	t.Helper()
	db, err := openAt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`CREATE TABLE schema_migrations (
		   version INTEGER PRIMARY KEY,
		   applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries { // embed.FS directory listings are sorted
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil || version > upTo {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("migration %s: %v", e.Name(), err)
		}
		if _, err := db.db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// Migration 008 renames the app_version columns to version and rewrites
// the attr_key rows earlier rollups stored — on a database that already
// holds data under the old names, none of it may be lost or left split
// across the two spellings.
func TestMigration008RenamesVersionData(t *testing.T) {
	db := newTestDBAt(t, 7)
	ctx := context.Background()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO product_events (id, project, event_name, actor_id, ts, received_at,
		platform, app_version, attributes) VALUES ('1','app','signup','u1',
		'2026-08-10T10:00:00Z','2026-08-10T10:00:01Z','ios','2.4.1','{}')`)
	mustExec(`INSERT INTO app_views (id, project, ts, received_at, actor_id, screen,
		platform, app_version) VALUES ('2','app','2026-08-10T10:00:00Z',
		'2026-08-10T10:00:01Z','u1','/home','ios','2.4.1')`)
	mustExec(`INSERT INTO agg_app_versions (project, day, platform, app_version, actives, views)
		VALUES ('app','2026-08-01','ios','2.3.0',5,9)`)
	mustExec(`INSERT INTO agg_product_attrs (project, day, event_name, attr_key, attr_value,
		count, unique_users) VALUES ('app','2026-08-01','signup','$app_version','2.3.0',4,3)`)

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var v string
	if err := db.db.QueryRow(`SELECT version FROM product_events WHERE id='1'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "2.4.1" {
		t.Errorf("product_events.version = %q, want 2.4.1", v)
	}
	if err := db.db.QueryRow(`SELECT version FROM app_views WHERE id='2'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "2.4.1" {
		t.Errorf("app_views.version = %q, want 2.4.1", v)
	}
	// The stitched views must see aggregated old-name data and raw rows
	// as one dimension under the new name.
	var actives int
	if err := db.db.QueryRow(`SELECT actives FROM v_app_versions
		WHERE day='2026-08-01' AND version='2.3.0'`).Scan(&actives); err != nil {
		t.Fatal(err)
	}
	if actives != 5 {
		t.Errorf("v_app_versions actives = %d, want 5", actives)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
		WHERE attr_key='$app_version'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d agg_product_attrs rows still under $app_version", n)
	}
	var count int
	if err := db.db.QueryRow(`SELECT count FROM v_product_attrs
		WHERE attr_key='$version' AND attr_value='2.3.0'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Errorf("v_product_attrs count under $version = %d, want 4", count)
	}
}
