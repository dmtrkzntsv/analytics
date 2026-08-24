package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

// DaysBefore drives the aggregation scheduler: it decides which days still
// have raw rows to roll up. Both raw tables must be reported independently.
func TestDaysBefore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hits := []store.WebHit{
		{ID: "1", Project: "app", TS: ts("2026-08-10T10:00:00Z"), VisitorHash: "v1", Path: "/"},
		{ID: "2", Project: "app", TS: ts("2026-08-10T11:00:00Z"), VisitorHash: "v2", Path: "/"},
		{ID: "3", Project: "app", TS: ts("2026-08-11T10:00:00Z"), VisitorHash: "v1", Path: "/"},
		{ID: "4", Project: "app", TS: ts("2026-08-20T10:00:00Z"), VisitorHash: "v1", Path: "/"},
		{ID: "5", Project: "other", TS: ts("2026-08-10T10:00:00Z"), VisitorHash: "v1", Path: "/"},
	}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	events := []store.ProductEvent{
		{ID: "e1", Project: "app", EventName: "signup", UserID: "u1", TS: ts("2026-08-12T10:00:00Z")},
		{ID: "e2", Project: "app", EventName: "signup", UserID: "u2", TS: ts("2026-08-12T11:00:00Z")},
		{ID: "e3", Project: "app", EventName: "signup", UserID: "u1", TS: ts("2026-08-20T10:00:00Z")},
	}
	if err := db.WriteProductEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	str := func(days []civil.Date) []string {
		out := make([]string, len(days))
		for i, d := range days {
			out[i] = d.String()
		}
		return out
	}
	web, err := db.WebDaysBefore(ctx, "app", day("2026-08-15"))
	if err != nil {
		t.Fatal(err)
	}
	// Distinct, sorted, project-scoped, and excluding the day on/after cutoff.
	if got := str(web); len(got) != 2 || got[0] != "2026-08-10" || got[1] != "2026-08-11" {
		t.Errorf("WebDaysBefore = %v, want [2026-08-10 2026-08-11]", got)
	}
	product, err := db.ProductDaysBefore(ctx, "app", day("2026-08-15"))
	if err != nil {
		t.Fatal(err)
	}
	if got := str(product); len(got) != 1 || got[0] != "2026-08-12" {
		t.Errorf("ProductDaysBefore = %v, want [2026-08-12]", got)
	}
	none, err := db.ProductDaysBefore(ctx, "missing", day("2026-08-15"))
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("unknown project = %v, want empty", str(none))
	}
}

// A malformed ts must surface as an error rather than a zero-value date that
// would silently misfile aggregates.
func TestDaysBeforeRejectsCorruptTimestamp(t *testing.T) {
	db := newTestDB(t)
	// Must sort before the cutoff so the WHERE clause admits it, yet be an
	// impossible calendar date so civil.Parse rejects it.
	if _, err := db.db.Exec(
		`INSERT INTO web_hits (id, project, ts, actor_id, path) VALUES ('x','app','2026-02-30T00:00:00Z','v','/')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WebDaysBefore(context.Background(), "app", day("2026-08-15")); err == nil {
		t.Fatal("want error for unparseable ts, got nil")
	}
}

// tx must leave no partial writes behind when fn fails; every write path
// depends on this for atomicity.
func TestTxRollsBackOnError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_web_daily VALUES ('app','2026-08-10',1,1,1,0,0)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_web_daily`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows survived a failed transaction, want 0", n)
	}
}

// After Close, every operation must return an error rather than panic — the
// scheduler keeps calling into the store while shutdown is in flight.
func TestOperationsOnClosedDB(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for name, op := range map[string]func() error{
		"PruneAggregates": func() error {
			return db.PruneAggregates(ctx, "app", day("2026-01-01"), day("2026-01-01"), day("2026-01-01"))
		},
		"IncrementalVacuum": func() error { return db.IncrementalVacuum(ctx) },
		"AggregateWebDay":   func() error { return db.AggregateWebDay(ctx, "app", day("2026-01-01")) },
		"Migrate":           func() error { return db.Migrate(ctx) },
		"WriteWebHits": func() error {
			return db.WriteWebHits(ctx, []store.WebHit{{ID: "1", Project: "app", TS: ts("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"}})
		},
		"WriteProductEvents": func() error {
			return db.WriteProductEvents(ctx, []store.ProductEvent{{ID: "e", Project: "app", EventName: "n", UserID: "u", TS: ts("2026-08-10T10:00:00Z")}})
		},
		"SyncProjects":  func() error { return db.SyncProjects(ctx, []store.ProjectInfo{{Alias: "app", Name: "App"}}) },
		"SetMeta":       func() error { return db.SetMeta(ctx, "k", "v") },
		"WebDaysBefore": func() error { _, err := db.WebDaysBefore(ctx, "app", day("2026-01-01")); return err },
		"ProjectAliases": func() error {
			_, err := db.ProjectAliases(ctx)
			return err
		},
		"GetMeta": func() error { _, err := db.GetMeta(ctx, "k"); return err },
	} {
		if err := op(); err == nil {
			t.Errorf("%s on a closed DB returned nil, want error", name)
		}
	}
}

func TestOpenRejectsBadDSNAndPath(t *testing.T) {
	if _, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "no-such-dir", "x.db")); err == nil {
		t.Error("want error opening a database under a missing directory")
	}
	if _, err := store.Open("sqlite://\x7f\x00"); err == nil {
		t.Error("want error for an unparseable DSN")
	}
}

// A migration that fails must roll back and must NOT be recorded as applied;
// a phantom version row would permanently skip the migration on restart,
// leaving the schema half-built.
func TestMigrateFailureIsNotRecorded(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// Collide with a table the migration creates, so 001 fails partway.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE agg_web_daily (nonsense TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Fatal("want error from a colliding migration, got nil")
	}
	var applied int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Errorf("%d migrations recorded after failure, want 0", applied)
	}
	// The failed migration must have rolled back its earlier statements too.
	var projects int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Error("failed migration left the projects table behind; rollback did not happen")
	}
}

// PruneAggregates must name the table it failed on, so an operator can tell
// which retention step broke.
func TestPruneAggregatesReportsFailingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_product_attrs`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneAggregates(ctx, "app", day("2026-01-01"), day("2026-01-01"), day("2026-01-01"))
	if err == nil {
		t.Fatal("want error when a target table is missing, got nil")
	}
	if !strings.Contains(err.Error(), "agg_product_attrs") {
		t.Errorf("error %q does not name the failing table", err)
	}
}

// A DSN with no leading slash puts the first path segment in url.Host; that
// relative form must resolve to a relative file, not be dropped.
func TestOpenRelativeDSN(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := store.Open("sqlite://sub/relative.db")
	if err == nil {
		s.Close()
		t.Fatal("want error: sub/ does not exist yet")
	}
	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open("sqlite://sub/relative.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("sub", "relative.db")); err != nil {
		t.Errorf("relative DSN did not create sub/relative.db: %v", err)
	}
}

// Empty batches are routine (a flush tick with nothing buffered) and must not
// open a transaction or error.
func TestWriteEmptyBatchIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, nil); err != nil {
		t.Errorf("WriteWebHits(nil) = %v", err)
	}
	if err := db.WriteProductEvents(ctx, nil); err != nil {
		t.Errorf("WriteProductEvents(nil) = %v", err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{}); err != nil {
		t.Errorf("WriteProductEvents(empty) = %v", err)
	}
}

func TestPruneAggregatesReportsFailingWebTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_web_utm`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneAggregates(ctx, "app", day("2026-01-01"), day("2026-01-01"), day("2026-01-01"))
	if err == nil || !strings.Contains(err.Error(), "agg_web_utm") {
		t.Errorf("error %v does not name the failing web table", err)
	}
}
