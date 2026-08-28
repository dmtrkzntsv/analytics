package sqlite

import (
	"context"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

// openRegistryDB creates a migrated store in t.TempDir. Mirrors the
// helper style used across this package's tests.
func openRegistryDB(t *testing.T) *DB {
	t.Helper()
	d, err := openAt(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreateProjectWritesAuditAndBumpsVersion(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	v0, err := d.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := store.RegistryProject{Alias: "blog", Name: "My blog",
		Identity: "anonymous", AllowedOrigins: `["https://blog.example.com"]`}
	if err := d.CreateProject(ctx, p, store.AuditEntry{
		Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	ps, _, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Alias != "blog" || ps[0].Identity != "anonymous" {
		t.Fatalf("LoadRegistry = %+v", ps)
	}
	v1, _ := d.ConfigVersion(ctx)
	if v1 != v0+1 {
		t.Errorf("config_version = %d, want %d", v1, v0+1)
	}
	var actor, action string
	if err := d.db.QueryRow(
		`SELECT actor, action FROM audit_log WHERE subject='blog'`).
		Scan(&actor, &action); err != nil {
		t.Fatal(err)
	}
	if actor != "cli" || action != "project.create" {
		t.Errorf("audit = %s %s", actor, action)
	}
}

func TestCreateProjectDuplicateAliasFails(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err == nil {
		t.Fatal("duplicate alias did not fail")
	}
}

func TestIngestKeyLifecycle(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	k := store.RegistryKey{Key: "ak_test1", Project: "blog", Label: "web"}
	if err := d.InsertIngestKey(ctx, k, store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetIngestKeyDisabled(ctx, "blog", "web", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	_, ks, err := d.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 || !ks[0].Disabled {
		t.Fatalf("keys = %+v, want one disabled", ks)
	}
	if err := d.SetIngestKeyDisabled(ctx, "blog", "web", false, store.AuditEntry{Actor: "cli", Action: "key.enable", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	_, ks, _ = d.LoadRegistry(ctx)
	if ks[0].Disabled {
		t.Fatal("key still disabled after enable")
	}
	// Unknown project+label is an error, not a silent no-op.
	if err := d.SetIngestKeyDisabled(ctx, "blog", "nope", true, store.AuditEntry{Actor: "cli", Action: "key.disable", Subject: "nope"}); err == nil {
		t.Fatal("unknown label did not fail")
	}
}

func TestArchiveRestoreProject(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetProjectArchived(ctx, "blog", true, store.AuditEntry{Actor: "mcp", Action: "project.archive", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	ps, _, _ := d.LoadRegistry(ctx)
	if !ps[0].Archived {
		t.Fatal("not archived")
	}
	if err := d.SetProjectArchived(ctx, "blog", false, store.AuditEntry{Actor: "mcp", Action: "project.restore", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	ps, _, _ = d.LoadRegistry(ctx)
	if ps[0].Archived {
		t.Fatal("still archived")
	}
}

func TestSetProjectArchivedErrors(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()

	// Archiving unknown alias should fail.
	if err := d.SetProjectArchived(ctx, "ghost", true, store.AuditEntry{Actor: "cli", Action: "project.archive", Subject: "ghost"}); err == nil {
		t.Fatal("archiving unknown alias did not fail")
	}

	// Create a project but don't archive it.
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}

	// Restore (archive=false) of non-archived project should be a no-op (nil error).
	if err := d.SetProjectArchived(ctx, "blog", false, store.AuditEntry{Actor: "mcp", Action: "project.restore", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}

	// Project should still be unarchived.
	ps, _, _ := d.LoadRegistry(ctx)
	if ps[0].Archived {
		t.Fatal("project was archived by restore no-op")
	}
}

// TestMigrationUpgradeFrom004 ensures a database populated before 005
// (with migrations 001-004 only) migrates cleanly and reads new columns
// with their defaults.
func TestMigrationUpgradeFrom004(t *testing.T) {
	d, err := openAt(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()

	// Manually apply migrations 001-004 only.
	if err := applyMigrationsUpTo(ctx, d, 4); err != nil {
		t.Fatal(err)
	}

	// Insert a project row using the old schema (before 005).
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO projects (id, alias, name) VALUES (?, ?, ?)`,
		"test-id", "old_project", "Old Project"); err != nil {
		t.Fatal(err)
	}

	// Now run the full migrate to apply 005.
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify the new columns exist with defaults.
	var identity, allowedOrigins string
	if err := d.db.QueryRowContext(ctx,
		`SELECT identity, allowed_origins FROM projects WHERE alias='old_project'`).
		Scan(&identity, &allowedOrigins); err != nil {
		t.Fatal(err)
	}

	if identity != "anonymous" {
		t.Errorf("identity = %q, want 'anonymous'", identity)
	}
	if allowedOrigins != "[]" {
		t.Errorf("allowed_origins = %q, want '[]'", allowedOrigins)
	}
}

// applyMigrationsUpTo applies migrations 001 through maxVersion to the database.
// It mimics the Migrate logic but stops at a specific version.
func applyMigrationsUpTo(ctx context.Context, d *DB, maxVersion int) error {
	if _, err := d.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version INTEGER PRIMARY KEY,
		   applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}

	// Read migration files.
	migrations := make(map[int][]byte)
	for _, e := range entries {
		var version int
		_, err := parseVersion(e.Name(), &version)
		if err != nil {
			continue
		}
		if version > maxVersion {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		migrations[version] = body
	}

	// Apply in sorted order.
	for v := 1; v <= maxVersion; v++ {
		body, ok := migrations[v]
		if !ok {
			continue
		}

		var done int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, v).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}

		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func TestDeleteProjectDataCascades(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	p := store.RegistryProject{Alias: "blog", Name: "a", Identity: "anonymous", AllowedOrigins: "[]"}
	if err := d.CreateProject(ctx, p, store.AuditEntry{Actor: "cli", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_d", Project: "blog", Label: "web"},
		store.AuditEntry{Actor: "cli", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
	// one row in a raw table and one in an aggregate table
	if _, err := d.db.Exec(`INSERT INTO web_hits (id, project, ts, received_at, actor_id, path,
		referrer_source, utm_source, utm_medium, utm_campaign, country, device, browser, os,
		user_id, group_id)
		VALUES ('h1','blog','2026-08-01T10:00:00Z','2026-08-01T10:00:00Z','a','/x','','','','','','','','','','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO agg_web_daily (project, day, visitors, pageviews,
		sessions, bounces, duration_sec) VALUES ('blog','2026-07-01',1,1,1,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProjectData(ctx, "blog", store.AuditEntry{
		Actor: "cli", Action: "project.delete", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"projects", "ingest_keys", "web_hits", "agg_web_daily"} {
		var c int
		if err := d.db.QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE ` + projectCol(table) + `='blog'`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Errorf("%s still has %d rows", table, c)
		}
	}
	// the audit row survives the deletion — that is the point of it
	var c int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='project.delete'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Error("no audit row for the delete")
	}
}

func projectCol(table string) string {
	if table == "projects" {
		return "alias"
	}
	return "project"
}

// parseVersion extracts the version number from a migration filename.
func parseVersion(name string, version *int) (string, error) {
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			if i == 0 {
				return "", nil
			}
			*version = 0
			for j := 0; j < i; j++ {
				*version = *version*10 + int(name[j]-'0')
			}
			return name[i:], nil
		}
	}
	return "", nil
}
