package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

// This file drives the SQL error branches that the happy-path tests in the
// rest of the package never touch: a missing table, a blocking trigger, or a
// duplicate key that INSERT OR IGNORE does not silently swallow. Every
// assertion checks the error text names the failing operation, not just that
// an error came back.

// --- aggregate_app.go ---

func TestAggregateAppDayFailsOnMissingDailyTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViews(t, db, store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", Screen: "/x"})
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_app_daily`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateAppDay(ctx, "p", appDay())
	if err == nil || !strings.Contains(err.Error(), "agg_app_daily") {
		t.Errorf("err = %v, want mention of agg_app_daily", err)
	}
}

func TestAggregateAppDayFailsOnMissingDimensionTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViews(t, db, store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", Screen: "/x", Country: "DE"})
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_app_countries`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateAppDay(ctx, "p", appDay())
	if err == nil || !strings.Contains(err.Error(), "agg_app_countries") {
		t.Errorf("err = %v, want mention of agg_app_countries", err)
	}
}

func TestAggregateAppDayFailsWhenRawDeleteBlocked(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViews(t, db, store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", Screen: "/x"})
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_app_delete BEFORE DELETE ON app_views
BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateAppDay(ctx, "p", appDay())
	if err == nil || !strings.Contains(err.Error(), "prune raw app_views") {
		t.Errorf("err = %v, want mention of prune raw app_views", err)
	}
}

// --- identities.go ---

func TestAggregateIdentityDayFailsOnMissingAggTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := at(10, 0)
	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a", UserID: "u1", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_identity_daily`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateIdentityDay(ctx, "p", appDay())
	if err == nil || !strings.Contains(err.Error(), "agg_identity_daily") {
		t.Errorf("err = %v, want mention of agg_identity_daily", err)
	}
}

func TestAggregateIdentityDayFailsUpdatingLastSeen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := at(10, 0)
	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a", UserID: "u1", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE identities`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateIdentityDay(ctx, "p", appDay())
	if err == nil || !strings.Contains(err.Error(), "identities last_seen") {
		t.Errorf("err = %v, want mention of identities last_seen", err)
	}
}

func TestPruneIdentitiesFailsOnMissingIdentitiesTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE identities`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneIdentities(ctx, "p", onDay(2026, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "prune identities") {
		t.Errorf("err = %v, want mention of prune identities", err)
	}
}

func TestPruneIdentitiesFailsOnMissingAggTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_identity_daily`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneIdentities(ctx, "p", onDay(2026, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "prune agg_identity_daily") {
		t.Errorf("err = %v, want mention of prune agg_identity_daily", err)
	}
}

// --- retention.go ---

func TestUpsertActorsFailsOnMissingActorsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE actors`); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "p", TS: ts("2026-08-10T10:00:00Z"), ActorID: "v1", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	// actorSources iterates app_views first, so that is the table named in
	// the error regardless of which raw table actually holds data.
	err := db.UpsertActors(ctx, "p", day("2026-08-10"))
	if err == nil || !strings.Contains(err.Error(), "upsert actors from app_views") {
		t.Errorf("err = %v, want mention of upsert actors from app_views", err)
	}
}

func TestAggregateRetentionDayFailsOnMissingAggTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_retention`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateRetentionDay(ctx, "p", day("2026-08-10"))
	if err == nil || !strings.Contains(err.Error(), "agg_retention") {
		t.Errorf("err = %v, want mention of agg_retention", err)
	}
}

func TestPruneActorsFailsOnMissingActorsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE actors`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneActors(ctx, "p", onDay(2026, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "prune actors") {
		t.Errorf("err = %v, want mention of prune actors", err)
	}
}

func TestPruneActorsFailsOnMissingRetentionTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_retention`); err != nil {
		t.Fatal(err)
	}
	err := db.PruneActors(ctx, "p", onDay(2026, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "prune agg_retention") {
		t.Errorf("err = %v, want mention of prune agg_retention", err)
	}
}

// --- aggregate_web.go ---

func TestAggregateWebDayFailsOnCountQuery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE web_hits`); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "p", day("2026-08-10")); err == nil {
		t.Error("want error counting a missing web_hits table")
	}
}

func TestAggregateWebDayFailsOnMissingDailyTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "p", TS: ts("2026-08-10T10:00:00Z"), ActorID: "v1", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_web_daily`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateWebDay(ctx, "p", day("2026-08-10"))
	if err == nil || !strings.Contains(err.Error(), "agg_web_daily") {
		t.Errorf("err = %v, want mention of agg_web_daily", err)
	}
}

func TestAggregateWebDayFailsOnMissingDimensionTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "p", TS: ts("2026-08-10T10:00:00Z"), ActorID: "v1", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_web_pages`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateWebDay(ctx, "p", day("2026-08-10"))
	if err == nil || !strings.Contains(err.Error(), "agg_web_pages") {
		t.Errorf("err = %v, want mention of agg_web_pages", err)
	}
}

func TestWebDaysBeforeFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE web_hits`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WebDaysBefore(ctx, "p", day("2026-08-10")); err == nil {
		t.Error("want error querying days from a missing table")
	}
}

// --- aggregate_product.go ---

func TestAggregateProductDayFailsOnCountQuery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE product_events`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateProductDay(ctx, "p", day("2026-08-10"), store.ProductAggSettings{})
	if err == nil {
		t.Error("want error counting a missing product_events table")
	}
}

func TestAggregateProductDayFailsOnDailyRollup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := ts("2026-08-10T10:00:00Z")
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "p", EventName: "signup", ActorID: "u1", TS: tstamp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_product_daily`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateProductDay(ctx, "p", day("2026-08-10"), store.ProductAggSettings{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "agg_product_daily") {
		t.Errorf("err = %v, want mention of agg_product_daily", err)
	}
}

func TestAggregateProductDayFailsOnTotalsRollup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := ts("2026-08-10T10:00:00Z")
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "p", EventName: "signup", ActorID: "u1", TS: tstamp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_product_totals`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateProductDay(ctx, "p", day("2026-08-10"), store.ProductAggSettings{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "agg_product_totals") {
		t.Errorf("err = %v, want mention of agg_product_totals", err)
	}
}

func TestAggregateProductDayFailsOnAttrRollup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := ts("2026-08-10T10:00:00Z")
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "p", EventName: "signup", ActorID: "u1", TS: tstamp,
			Attributes: map[string]string{"plan": "pro"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE agg_product_attrs`); err != nil {
		t.Fatal(err)
	}
	agg := store.ProductAggSettings{Enabled: true, Attributes: map[string][]string{"*": {"plan"}}, TopN: 10}
	err := db.AggregateProductDay(ctx, "p", day("2026-08-10"), agg)
	if err == nil || !strings.Contains(err.Error(), "attr signup/plan") {
		t.Errorf("err = %v, want mention of attr signup/plan", err)
	}
}

func TestAggregateProductDayFailsOnRawDeleteBlocked(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := ts("2026-08-10T10:00:00Z")
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "p", EventName: "signup", ActorID: "u1", TS: tstamp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_product_delete BEFORE DELETE ON product_events
BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.AggregateProductDay(ctx, "p", day("2026-08-10"), store.ProductAggSettings{})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("err = %v, want mention of the blocking trigger", err)
	}
}

// --- flatview.go ---

func TestKnownAttributeKeysFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE product_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.KnownAttributeKeys(ctx); err == nil {
		t.Error("want error querying a missing product_events table")
	}
}

// SQLite views are validated lazily (at query time, not creation time), so
// a missing product_events table cannot make CREATE VIEW itself fail; the
// reachable failure is DROP VIEW hitting an object of the wrong type.
func TestRebuildFlatViewFailsWhenNameIsATable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE v_events_flat (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	err := db.RebuildFlatView(ctx, []string{"plan"})
	if err == nil || !strings.Contains(err.Error(), "drop v_events_flat") {
		t.Errorf("err = %v, want mention of drop v_events_flat", err)
	}
}

// --- registry.go ---

func TestConfigVersionFailsOnMissingMetaTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigVersion(ctx); err == nil {
		t.Error("want error reading config_version from a missing meta table")
	}
}

func TestConfigVersionDefaultsToZeroWhenUnset(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DELETE FROM meta WHERE key='config_version'`); err != nil {
		t.Fatal(err)
	}
	v, err := db.ConfigVersion(ctx)
	if err != nil {
		t.Fatalf("ConfigVersion = %v, want nil error", err)
	}
	if v != 0 {
		t.Errorf("ConfigVersion = %d, want 0 when the row is absent", v)
	}
}

func TestAuditAndBumpFailsOnMissingAuditLogTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"})
	if err == nil {
		t.Error("want error writing the audit row to a missing audit_log table")
	}
}

func TestAuditAndBumpFailsWhenMetaMissing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE meta`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"})
	if err == nil {
		t.Error("want error bumping config_version in a missing meta table")
	}
	// The audit_log insert (which happens before the bump) must have rolled
	// back along with everything else in the transaction.
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("audit_log rows = %d after a failed bump, want 0 (transaction not rolled back)", n)
	}
}

func TestLoadRegistryFailsOnMissingProjectsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LoadRegistry(ctx); err == nil {
		t.Error("want error listing projects from a missing table")
	}
}

func TestLoadRegistryFailsOnMissingIngestKeysTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LoadRegistry(ctx); err == nil {
		t.Error("want error listing ingest keys from a missing table")
	}
}

func TestUpdateProjectFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	err := db.UpdateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.update", Subject: "blog"})
	if err == nil {
		t.Error("want error updating a missing projects table")
	}
}

func TestSetProjectArchivedFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	err := db.SetProjectArchived(ctx, "blog", true, store.AuditEntry{Actor: "cli", Action: "project.archive", Subject: "blog"})
	if err == nil {
		t.Error("want error archiving against a missing projects table")
	}
}

func TestInsertIngestKeyFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_x", Project: "blog", Label: "web"}
	err := db.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"})
	if err == nil {
		t.Error("want error checking for an existing key in a missing table")
	}
}

func TestInsertIngestKeyFailsOnDuplicateLabel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_1", Project: "blog", Label: "web"}
	if err := db.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	dup := store.RegistryKey{Key: "ak_2", Project: "blog", Label: "web"}
	err := db.InsertIngestKey(ctx, dup, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"})
	if err == nil || !strings.Contains(err.Error(), `already exists`) {
		t.Errorf("err = %v, want mention that the label already exists", err)
	}
}

func TestInsertIngestKeyFailsOnDuplicateKeyValue(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_dup", Project: "blog", Label: "web"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	// Same key value, different label: the label-uniqueness check passes,
	// so this must fail at the INSERT on the primary key instead.
	err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_dup", Project: "blog", Label: "mobile"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "mobile"})
	if err == nil || !strings.Contains(err.Error(), "issue key for") {
		t.Errorf("err = %v, want mention of issue key for", err)
	}
}

func TestSetIngestKeyDisabledFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE ingest_keys`); err != nil {
		t.Fatal(err)
	}
	err := db.SetIngestKeyDisabled(ctx, "blog", "web", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "web"})
	if err == nil {
		t.Error("want error disabling a key in a missing table")
	}
}

func TestDeleteProjectDataFailsOnMissingProjectsTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	err := db.DeleteProjectData(ctx, "blog", store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "blog"})
	if err == nil {
		t.Error("want error deleting from a missing projects table")
	}
}

func TestDeleteProjectDataFailsOnUnknownAlias(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.DeleteProjectData(ctx, "ghost", store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown alias") {
		t.Errorf("err = %v, want mention of unknown alias", err)
	}
}

func TestDeleteProjectDataFailsOnMissingChildTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := db.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TABLE web_hits`); err != nil {
		t.Fatal(err)
	}
	err := db.DeleteProjectData(ctx, "blog", store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "blog"})
	if err == nil || !strings.Contains(err.Error(), "delete web_hits") {
		t.Errorf("err = %v, want mention of delete web_hits", err)
	}
}

// --- sqlite.go ---

func TestOpenFailsOnInvalidURLEscape(t *testing.T) {
	if _, err := open("sqlite://%zz"); err == nil {
		t.Error("want error parsing an invalid percent-escape in the DSN")
	}
}

func TestOpenAtFailsOnReadonlyFile(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/ro.db"
	if err := os.WriteFile(p, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(p); err == nil {
		t.Error("want error setting auto_vacuum on a readonly file")
	}
}

// --- write.go ---

func TestWriteWebHitsFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE web_hits`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteWebHits(ctx, []store.WebHit{{ID: "1", Project: "p", TS: ts("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"}})
	if err == nil {
		t.Error("want error preparing an insert against a missing table")
	}
}

func TestWriteWebHitsFailsOnBlockedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_web_hit BEFORE INSERT ON web_hits
WHEN NEW.id = 'blocked'
BEGIN SELECT RAISE(ABORT, 'blocked by trigger'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteWebHits(ctx, []store.WebHit{{ID: "blocked", Project: "p", TS: ts("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"}})
	if err == nil || !strings.Contains(err.Error(), "web hit blocked") {
		t.Errorf("err = %v, want mention of web hit blocked", err)
	}
}

func TestWriteProductEventsFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE product_events`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "p", EventName: "signup", ActorID: "u", TS: ts("2026-08-10T10:00:00Z")}})
	if err == nil {
		t.Error("want error preparing an insert against a missing table")
	}
}

func TestWriteProductEventsFailsOnBlockedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_event BEFORE INSERT ON product_events
WHEN NEW.id = 'blocked'
BEGIN SELECT RAISE(ABORT, 'blocked by trigger'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "blocked", Project: "p", EventName: "signup", ActorID: "u", TS: ts("2026-08-10T10:00:00Z")}})
	if err == nil || !strings.Contains(err.Error(), "event blocked") {
		t.Errorf("err = %v, want mention of event blocked", err)
	}
}

func TestWriteAppViewsFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE app_views`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteAppViews(ctx, []store.AppView{{ID: "1", Project: "p", TS: at(10, 0), ActorID: "a", Screen: "/x"}})
	if err == nil {
		t.Error("want error preparing an insert against a missing table")
	}
}

func TestWriteAppViewsFailsOnBlockedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_view BEFORE INSERT ON app_views
WHEN NEW.id = 'blocked'
BEGIN SELECT RAISE(ABORT, 'blocked by trigger'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.WriteAppViews(ctx, []store.AppView{{ID: "blocked", Project: "p", TS: at(10, 0), ActorID: "a", Screen: "/x"}})
	if err == nil || !strings.Contains(err.Error(), "app view blocked") {
		t.Errorf("err = %v, want mention of app view blocked", err)
	}
}

func TestUpsertIdentitiesFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE identities`); err != nil {
		t.Fatal(err)
	}
	err := db.UpsertIdentities(ctx, []store.Identity{{Project: "p", Kind: store.KindUser, ID: "u1", Name: "Ada"}})
	if err == nil {
		t.Error("want error preparing an upsert against a missing table")
	}
}

func TestUpsertIdentitiesFailsOnBlockedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER block_identity BEFORE INSERT ON identities
WHEN NEW.id = 'blocked'
BEGIN SELECT RAISE(ABORT, 'blocked by trigger'); END`); err != nil {
		t.Fatal(err)
	}
	err := db.UpsertIdentities(ctx, []store.Identity{{Project: "p", Kind: store.KindUser, ID: "blocked", Name: "Ada"}})
	if err == nil || !strings.Contains(err.Error(), "identity user/blocked") {
		t.Errorf("err = %v, want mention of identity user/blocked", err)
	}
}

func TestProjectAliasesFailsOnMissingTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProjectAliases(ctx); err == nil {
		t.Error("want error listing aliases from a missing table")
	}
}

// --- migrate.go ---

func TestMigrateFailsOnLookupQuery(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// A view named schema_migrations satisfies Migrate's own "IF NOT
	// EXISTS" guard (the name already exists), so the CREATE TABLE step
	// no-ops; but SQLite validates views lazily, so the very next SELECT
	// against it fails because the view's underlying table doesn't exist.
	if _, err := db.db.ExecContext(ctx,
		`CREATE VIEW schema_migrations AS SELECT * FROM nonexistent_table`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Error("want error querying schema_migrations through a broken view")
	}
}

func TestMigrateFailsInsertingSchemaVersion(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// Pre-create schema_migrations with an extra NOT NULL column that has
	// no default. The lookup SELECT only reads version, so it still works;
	// Migrate's own CREATE TABLE IF NOT EXISTS then no-ops on the existing
	// table, and the migration body itself succeeds, but the final INSERT
	// (which supplies only version) then violates the extra column.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')),
		extra TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Error("want error inserting into a schema_migrations missing a required column's value")
	}
	// The migration body's own tables must have rolled back with the
	// failed INSERT, or a retry would find them already there and error
	// on a duplicate CREATE TABLE.
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("migration 001's tables survived a failed schema_migrations insert; rollback did not happen")
	}
}
