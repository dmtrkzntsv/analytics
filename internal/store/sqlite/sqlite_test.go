package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := openAt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenViaRegistry(t *testing.T) {
	s, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPragmas(t *testing.T) {
	db := newTestDB(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"auto_vacuum":  "2", // INCREMENTAL
	} {
		var got string
		if err := db.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("schema_migrations rows = %d", n)
	}
}

func TestSchemaTablesExist(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{
		"meta", "projects", "web_hits", "product_events",
		"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
		"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
		"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	} {
		var name string
		err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}
