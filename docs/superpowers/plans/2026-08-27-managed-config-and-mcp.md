# Managed Configuration + MCP Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move project configuration into the database with CLI + MCP management, then expose an authenticated read/manage MCP endpoint over the existing views.

**Architecture:** Phase A (Tasks 1–12) implements `2026-08-27-managed-config-design.md`: migration 005 adds the registry tables; `internal/manage` owns an atomic snapshot (constant-time key lookup, 1s `config_version` poll) and all mutating operations with audit; `internal/config` loses `PROJECTS_FILE`; server/jobs/app consume the snapshot; CLI subcommands `project`/`key`/`config` are thin frontends. Phase B (Tasks 13–22) implements `2026-08-24-mcp-endpoint-design.md`: `internal/mcpserver` serves Streamable HTTP MCP behind three interchangeable token verifiers, with nine read tools over the `v_*` views plus a four-layer-guarded SQL escape hatch, eight management tools over `internal/manage`, and two schema resources; `serve` gains `-api`/`-mcp` and bare `serve` becomes a usage error.

**Tech Stack:** Go 1.25 stdlib; modernc.org/sqlite (existing); github.com/modelcontextprotocol/go-sdk v1.7.0 and github.com/golang-jwt/jwt/v5 (new, Phase B only).

**Specs:** `docs/superpowers/specs/2026-08-27-managed-config-design.md` (Phase A), `docs/superpowers/specs/2026-08-24-mcp-endpoint-design.md` (Phase B). The plan argues from the specs; executors read both.

## Global Constraints

- Coverage: every core package ≥ 85% (`make check` runs `scripts/coverage.sh`); `make build-all`, `go vet ./...`, `gofmt -l .` clean at every commit.
- Never log request bodies, IPs, User-Agents, bearer tokens, or submitted SQL at `info` (submitted SQL is `debug`-only).
- No new dependencies in Phase A. Phase B adds exactly `github.com/modelcontextprotocol/go-sdk v1.7.0` + `github.com/golang-jwt/jwt/v5` (direct).
- Key/token entropy: ingest keys `ak_` + 16 random bytes hex (existing); MCP token `ar_` + 32 random bytes hex.
- Constant-time credential comparison: `crypto/subtle.ConstantTimeCompare` over a slice, no early exit on match — same pattern as the current `config.ProjectByKey`.
- All timestamps in the DB are UTC ISO-8601 TEXT; `day` columns are `YYYY-MM-DD` TEXT.
- Identity modes are exactly `anonymous` | `identified` (`config.IdentityAnonymous` / `config.IdentityIdentified`).
- Migration files execute in lexical order inside one transaction each (see `internal/store/sqlite/migrate.go`); the new migration is `005_registry.sql`.
- Commit messages: conventional style (`feat:`, `test:`, `docs:`, `refactor:`) matching `git log`.

## File Structure

Phase A:

    internal/store/sqlite/migrations/005_registry.sql   registry schema
    internal/store/sqlite/registry.go                   row access + audited writes
    internal/store/store.go                             interface additions; SyncProjects/ProjectInfo removed
    internal/manage/registry.go                         Snapshot, atomic swap, version poll
    internal/manage/ops.go                              audited operations + key minting
    internal/manage/importexport.go                     JSON round-trip + legacy import
    internal/config/config.go                           PROJECTS_FILE removal; Project kept as import format
    internal/server/{server,handlers}.go                consume Snapshot instead of Config
    internal/jobs/jobs.go                               consume Snapshot
    internal/app/app.go                                 registry wiring; SyncProjects call removed
    cmd/analytics/{project,key,configcmd}.go            CLI frontends
    cmd/analytics/keygen.go                             -mcp flag; ingest mode deprecated
    scripts/smoke.sh, deploy/*, .env.example, README.md deploy story

Phase B:

    internal/mcpserver/server.go        assembly: mcp.Server, routes, middleware
    internal/mcpserver/auth.go          three TokenVerifiers + JWKS cache
    internal/mcpserver/readdb.go        read-only pool + row scanning
    internal/mcpserver/tools_read.go    list_projects, web/app overview+breakdown
    internal/mcpserver/tools_product.go product_events, product_attributes, retention, identities
    internal/mcpserver/tools_manage.go  eight management tools
    internal/mcpserver/query.go         guarded SQL
    internal/mcpserver/resources.go     schema://views, schema://projects
    internal/config/config.go           MCP_* parsing + fail-fast validation
    internal/app/app.go                 two listeners, shared-mux rule
    cmd/analytics/serve.go              -api / -mcp flags, bare-serve usage error
    Dockerfile, deploy/systemd/analytics.service, Makefile, scripts/smoke.sh, README.md

---

## Phase A — Managed configuration

### Task 1: Migration 005 and registry row access

**Files:**
- Create: `internal/store/sqlite/migrations/005_registry.sql`
- Create: `internal/store/sqlite/registry.go`
- Create: `internal/store/sqlite/registry_test.go`
- Modify: `internal/store/store.go` (interface additions only in this task)

**Interfaces:**
- Consumes: `d.tx(ctx, func(tx *sql.Tx) error) error` helper (exists in `internal/store/sqlite/write.go`), `meta` table, `projects` table.
- Produces (used by Tasks 2–5, 9–11):

```go
// in package store
type RegistryProject struct {
    Alias, Name, Identity string
    AllowedOrigins        string // JSON array, "[]" if none
    Retention             string // JSON object or ""
    Aggregation           string // JSON object or ""
    Archived              bool
}
type RegistryKey struct {
    Key, Project, Label string
    Disabled            bool
}
type AuditEntry struct {
    Actor, Action, Subject, Detail string
}
// added to the Store interface:
LoadRegistry(ctx context.Context) ([]RegistryProject, []RegistryKey, error)
ConfigVersion(ctx context.Context) (int64, error)
CreateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
UpdateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
SetProjectArchived(ctx context.Context, alias string, archived bool, a AuditEntry) error
InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
SetIngestKeyDisabled(ctx context.Context, project, label string, disabled bool, a AuditEntry) error
DeleteProjectData(ctx context.Context, alias string, a AuditEntry) error  // added in Task 5, listed for completeness
```

- [ ] **Step 1: Write the migration**

`internal/store/sqlite/migrations/005_registry.sql`:

```sql
-- Registry: project configuration moves from projects.json into the
-- database (managed-config spec §3.1). The database is the sole runtime
-- source of truth; SyncProjects and its boot-archiving are deleted.

ALTER TABLE projects ADD COLUMN identity            TEXT NOT NULL DEFAULT 'anonymous';
ALTER TABLE projects ADD COLUMN allowed_origins     TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN retention           TEXT;
ALTER TABLE projects ADD COLUMN product_aggregation TEXT;

CREATE TABLE ingest_keys (
    key         TEXT PRIMARY KEY,
    project     TEXT NOT NULL REFERENCES projects(alias),
    label       TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    disabled_at TEXT                                      -- NULL = active
);
CREATE INDEX idx_ingest_keys_project ON ingest_keys(project);

CREATE TABLE audit_log (
    ts      TEXT NOT NULL DEFAULT (datetime('now')),
    actor   TEXT NOT NULL,          -- 'mcp' or 'cli'
    action  TEXT NOT NULL,          -- 'project.create', 'key.disable', ...
    subject TEXT NOT NULL,          -- alias or key label
    detail  TEXT NOT NULL DEFAULT ''
);

INSERT INTO meta (key, value) VALUES ('config_version', '1')
ON CONFLICT(key) DO NOTHING;
```

- [ ] **Step 2: Write the failing tests**

`internal/store/sqlite/registry_test.go`:

```go
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
```

Also add a migration-upgrade case: the existing `zz_seed_test.go` seeds a database through `Migrate`; add one assertion there that a database populated before 005 (create tables via migrations 001–004 by copying `Migrate`'s loop bounded to version 4 in a test helper, insert one `projects` row) migrates cleanly and the new columns read back with their defaults (`identity='anonymous'`, `allowed_origins='[]'`).

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/store/sqlite/ -run 'TestCreateProject|TestIngestKey|TestArchiveRestore' -v`
Expected: FAIL — `d.ConfigVersion undefined`, `store.RegistryProject undefined`.

- [ ] **Step 4: Add the types and interface methods to `internal/store/store.go`**

Add the three structs from the Interfaces block above after `ProjectInfo`, and the eight method signatures to the `Store` interface after `SetMeta`. (Do NOT remove `SyncProjects` yet — that happens in Task 8 when `app.Serve` stops calling it.)

- [ ] **Step 5: Implement `internal/store/sqlite/registry.go`**

```go
// Registry row access (managed-config spec §3). Every write bumps
// meta.config_version and inserts its audit row in the same transaction,
// which is what lets other processes notice changes by polling one row.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/store"
	"github.com/google/uuid"
)

func auditAndBump(ctx context.Context, tx *sql.Tx, a store.AuditEntry) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, subject, detail) VALUES (?,?,?,?)`,
		a.Actor, a.Action, a.Subject, a.Detail); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = 'config_version'`)
	return err
}

func (d *DB) ConfigVersion(ctx context.Context) (int64, error) {
	var v int64
	err := d.db.QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key='config_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

func (d *DB) LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT alias, name, identity,
		allowed_origins, COALESCE(retention,''), COALESCE(product_aggregation,''),
		archived_at IS NOT NULL FROM projects ORDER BY alias`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []store.RegistryProject
	for rows.Next() {
		var p store.RegistryProject
		if err := rows.Scan(&p.Alias, &p.Name, &p.Identity,
			&p.AllowedOrigins, &p.Retention, &p.Aggregation, &p.Archived); err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	krows, err := d.db.QueryContext(ctx, `SELECT key, project, label,
		disabled_at IS NOT NULL FROM ingest_keys ORDER BY project, label`)
	if err != nil {
		return nil, nil, err
	}
	defer krows.Close()
	var ks []store.RegistryKey
	for krows.Next() {
		var k store.RegistryKey
		if err := krows.Scan(&k.Key, &k.Project, &k.Label, &k.Disabled); err != nil {
			return nil, nil, err
		}
		ks = append(ks, k)
	}
	return ks2(ps, ks, krows.Err())
}

// ks2 keeps the happy-path return on one line above.
func ks2(ps []store.RegistryProject, ks []store.RegistryKey, err error) ([]store.RegistryProject, []store.RegistryKey, error) {
	if err != nil {
		return nil, nil, err
	}
	return ps, ks, nil
}

func (d *DB) CreateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects
			(id, alias, name, identity, allowed_origins, retention, product_aggregation)
			VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`,
			id.String(), p.Alias, p.Name, p.Identity, p.AllowedOrigins,
			p.Retention, p.Aggregation); err != nil {
			return fmt.Errorf("create project %q: %w", p.Alias, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, identity=?,
			allowed_origins=?, retention=NULLIF(?,''), product_aggregation=NULLIF(?,'')
			WHERE alias=?`,
			p.Name, p.Identity, p.AllowedOrigins, p.Retention, p.Aggregation, p.Alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("update project: unknown alias %q", p.Alias)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetProjectArchived(ctx context.Context, alias string, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE projects SET archived_at=datetime('now') WHERE alias=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE projects SET archived_at=NULL WHERE alias=?`
		}
		res, err := tx.ExecContext(ctx, q, alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 && archived {
			// restore of a non-archived project is a no-op, archive of an
			// unknown alias is an error; check existence to distinguish.
			var c int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM projects WHERE alias=?`, alias).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return fmt.Errorf("archive: unknown alias %q", alias)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) InsertIngestKey(ctx context.Context, k store.RegistryKey, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ingest_keys (key, project, label) VALUES (?,?,?)`,
			k.Key, k.Project, k.Label); err != nil {
			return fmt.Errorf("issue key for %q: %w", k.Project, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetIngestKeyDisabled(ctx context.Context, project, label string, disabled bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE ingest_keys SET disabled_at=datetime('now') WHERE project=? AND label=?`
		if !disabled {
			q = `UPDATE ingest_keys SET disabled_at=NULL WHERE project=? AND label=?`
		}
		res, err := tx.ExecContext(ctx, q, project, label)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("key %s/%s not found", project, label)
		}
		return auditAndBump(ctx, tx, a)
	})
}
```

`DeleteProjectData` is intentionally absent from this task: its interface method and implementation are added together in Task 5, so no stub ever exists.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/store/sqlite/ -run 'TestCreateProject|TestIngestKey|TestArchiveRestore' -v`
Expected: PASS. Then `go build ./...` — the rest of the tree still compiles because nothing was removed.

- [ ] **Step 7: Commit**

```bash
git add internal/store/sqlite/migrations/005_registry.sql internal/store/sqlite/registry.go internal/store/sqlite/registry_test.go internal/store/store.go
git commit -m "feat(store): registry schema and audited row access (migration 005)"
```

### Task 2: `internal/manage` — Snapshot and Registry

**Files:**
- Create: `internal/manage/registry.go`
- Create: `internal/manage/registry_test.go`

**Interfaces:**
- Consumes: `store.Store` (`LoadRegistry`, `ConfigVersion` from Task 1), `config.Retention`, `config.RetentionOverride`, `config.ProductAggregation`, `store.ProductAggSettings`.
- Produces (used by server, jobs, mcpserver, CLI):

```go
type Project struct {
    Alias, Name, Identity string
    AllowedOrigins        []string
    Retention             *config.RetentionOverride
    Aggregation           *config.ProductAggregation
    Archived              bool
}
func New(st store.Store, defaults config.Retention, logger *slog.Logger) *Registry
func (r *Registry) Reload(ctx context.Context) error
func (r *Registry) Snapshot(ctx context.Context) *Snapshot   // ≤1/s version poll
func (s *Snapshot) Project(alias string) *Project            // nil if unknown
func (s *Snapshot) Projects() []*Project                     // sorted by alias, incl. archived
func (s *Snapshot) ProjectByKey(key string) (*Project, string, bool)
func (s *Snapshot) OriginAllowed(alias, origin string) bool
func (s *Snapshot) AnyOriginAllowed(origin string) bool
func (s *Snapshot) RetentionFor(alias string) config.Retention
func (s *Snapshot) AggregationFor(alias string) store.ProductAggSettings
```

- [ ] **Step 1: Write the failing tests**

`internal/manage/registry_test.go`:

```go
package manage

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

var defaults = config.Retention{
	Web:     config.RetentionClass{RawDays: 7, AggregateDays: 365},
	Product: config.RetentionClass{RawDays: 30, AggregateDays: 365},
	App:     config.RetentionClass{RawDays: 30, AggregateDays: 365},
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedProject(t *testing.T, st store.Store, alias string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, store.RegistryProject{
		Alias: alias, Name: alias, Identity: "identified",
		AllowedOrigins: `["https://` + alias + `.example.com"]`,
		Retention:      `{"web":{"raw_days":90}}`,
	}, store.AuditEntry{Actor: "test", Action: "project.create", Subject: alias}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIngestKey(ctx, store.RegistryKey{
		Key: "ak_" + alias, Project: alias, Label: "web",
	}, store.AuditEntry{Actor: "test", Action: "key.issue", Subject: "web"}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotLookups(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	reg := New(st, defaults, slog.Default())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(context.Background())

	p, label, ok := s.ProjectByKey("ak_blog")
	if !ok || p.Alias != "blog" || label != "web" {
		t.Fatalf("ProjectByKey = %v %q %v", p, label, ok)
	}
	if _, _, ok := s.ProjectByKey("ak_nope"); ok {
		t.Fatal("unknown key resolved")
	}
	if !s.OriginAllowed("blog", "https://blog.example.com") {
		t.Fatal("origin not allowed")
	}
	if s.OriginAllowed("blog", "https://evil.example.com") {
		t.Fatal("wrong origin allowed")
	}
	r := s.RetentionFor("blog")
	if r.Web.RawDays != 90 || r.Web.AggregateDays != 365 || r.Product.RawDays != 30 {
		t.Fatalf("RetentionFor merged wrong: %+v", r)
	}
	// Unknown alias falls back to defaults, matching the archived-project
	// behaviour jobs relies on.
	if r := s.RetentionFor("ghost"); r != defaults {
		t.Fatalf("RetentionFor(ghost) = %+v", r)
	}
	if p := s.Project("blog"); p == nil || p.Identity != "identified" {
		t.Fatalf("Project = %+v", p)
	}
}

func TestArchivedProjectKeysDoNotResolve(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	ctx := context.Background()
	if err := st.SetProjectArchived(ctx, "blog", true,
		store.AuditEntry{Actor: "test", Action: "project.archive", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	reg := New(st, defaults, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)
	if _, _, ok := s.ProjectByKey("ak_blog"); ok {
		t.Fatal("archived project's key resolved")
	}
	// but the project itself is still visible (retention overrides survive)
	if p := s.Project("blog"); p == nil || !p.Archived {
		t.Fatalf("Project = %+v", p)
	}
}

func TestSnapshotPicksUpOutOfProcessWrite(t *testing.T) {
	st := testStore(t)
	seedProject(t, st, "blog")
	reg := New(st, defaults, slog.Default())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a CLI in another process: write via the store directly.
	seedProject(t, st, "docs")
	if p := reg.Snapshot(ctx).Project("docs"); p != nil {
		t.Fatal("saw the write before the poll interval elapsed")
	}
	reg.lastCheck.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if p := reg.Snapshot(ctx).Project("docs"); p == nil {
		t.Fatal("poll did not pick up the write")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/manage/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/manage/registry.go`**

```go
// Package manage owns the project registry (managed-config spec §3–§4):
// an immutable snapshot behind an atomic pointer for the ingest hot path,
// and the audited operations that mutate it. MCP tools, CLI subcommands
// and the importer are thin frontends over this package.
package manage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type Project struct {
	Alias, Name, Identity string
	AllowedOrigins        []string
	Retention             *config.RetentionOverride
	Aggregation           *config.ProductAggregation
	Archived              bool
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}

// Snapshot is an immutable view of the registry. Readers pay one atomic
// load; every mutation builds a fresh one.
type Snapshot struct {
	byAlias  map[string]*Project
	ordered  []*Project
	keys     []keyOwner // active keys of non-archived projects only
	origins  map[string]map[string]bool
	defaults config.Retention
}

// pollInterval bounds how often the hot path re-reads config_version to
// notice out-of-process writes (CLI, second serve process). Spec §3.3.
const pollInterval = time.Second

type Registry struct {
	st       store.Store
	defaults config.Retention
	logger   *slog.Logger

	snap      atomic.Pointer[Snapshot]
	version   atomic.Int64
	lastCheck atomic.Int64 // UnixNano of the last version poll
}

func New(st store.Store, defaults config.Retention, logger *slog.Logger) *Registry {
	r := &Registry{st: st, defaults: defaults, logger: logger}
	r.snap.Store(&Snapshot{byAlias: map[string]*Project{}, origins: map[string]map[string]bool{}, defaults: defaults})
	return r
}

// Reload rebuilds the snapshot from the database. Called at boot, after
// every in-process write, and by the poll when config_version moves.
func (r *Registry) Reload(ctx context.Context) error {
	ps, ks, err := r.st.LoadRegistry(ctx)
	if err != nil {
		return fmt.Errorf("manage: load registry: %w", err)
	}
	v, err := r.st.ConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("manage: config version: %w", err)
	}
	s := &Snapshot{
		byAlias:  make(map[string]*Project, len(ps)),
		origins:  make(map[string]map[string]bool, len(ps)),
		defaults: r.defaults,
	}
	for _, rp := range ps {
		p := &Project{Alias: rp.Alias, Name: rp.Name, Identity: rp.Identity, Archived: rp.Archived}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &p.AllowedOrigins); err != nil {
				return fmt.Errorf("manage: project %q allowed_origins: %w", rp.Alias, err)
			}
		}
		if rp.Retention != "" {
			p.Retention = new(config.RetentionOverride)
			if err := json.Unmarshal([]byte(rp.Retention), p.Retention); err != nil {
				return fmt.Errorf("manage: project %q retention: %w", rp.Alias, err)
			}
		}
		if rp.Aggregation != "" {
			p.Aggregation = new(config.ProductAggregation)
			if err := json.Unmarshal([]byte(rp.Aggregation), p.Aggregation); err != nil {
				return fmt.Errorf("manage: project %q product_aggregation: %w", rp.Alias, err)
			}
			if p.Aggregation.TopN == 0 {
				p.Aggregation.TopN = 50
			}
		}
		s.byAlias[p.Alias] = p
		s.ordered = append(s.ordered, p)
		set := map[string]bool{}
		for _, o := range p.AllowedOrigins {
			set[trimSlash(o)] = true
		}
		s.origins[p.Alias] = set
	}
	for _, k := range ks {
		p := s.byAlias[k.Project]
		if p == nil || p.Archived || k.Disabled {
			continue // archived projects reject events (001_init.sql comment)
		}
		s.keys = append(s.keys, keyOwner{key: k.Key, project: p, label: k.Label})
	}
	r.snap.Store(s)
	r.version.Store(v)
	r.lastCheck.Store(time.Now().UnixNano())
	return nil
}

// Snapshot returns the current registry view, polling config_version at
// most once per pollInterval to notice out-of-process writes. On poll
// failure the previous snapshot keeps serving: a transient read error
// must not take down ingestion.
func (r *Registry) Snapshot(ctx context.Context) *Snapshot {
	last := r.lastCheck.Load()
	now := time.Now().UnixNano()
	if now-last >= int64(pollInterval) && r.lastCheck.CompareAndSwap(last, now) {
		if v, err := r.st.ConfigVersion(ctx); err != nil {
			r.logger.Warn("registry version poll failed", "error", err)
		} else if v != r.version.Load() {
			if err := r.Reload(ctx); err != nil {
				r.logger.Warn("registry reload failed", "error", err)
			}
		}
	}
	return r.snap.Load()
}

func trimSlash(o string) string {
	if len(o) > 0 && o[len(o)-1] == '/' {
		return o[:len(o)-1]
	}
	return o
}

func (s *Snapshot) Project(alias string) *Project { return s.byAlias[alias] }

func (s *Snapshot) Projects() []*Project { return s.ordered }

// ProjectByKey preserves the constant-time contract of the old
// config.ProjectByKey: every candidate compared, no early return.
func (s *Snapshot) ProjectByKey(key string) (*Project, string, bool) {
	if key == "" {
		return nil, "", false
	}
	match := -1
	kb := []byte(key)
	for i := range s.keys {
		if subtle.ConstantTimeCompare([]byte(s.keys[i].key), kb) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, "", false
	}
	return s.keys[match].project, s.keys[match].label, true
}

func (s *Snapshot) OriginAllowed(alias, origin string) bool {
	set, ok := s.origins[alias]
	return ok && set[trimSlash(origin)]
}

func (s *Snapshot) AnyOriginAllowed(origin string) bool {
	o := trimSlash(origin)
	for _, set := range s.origins {
		if set[o] {
			return true
		}
	}
	return false
}

// RetentionFor merges the project's override over the global defaults,
// byte-for-byte the same semantics as the old config.RetentionFor.
func (s *Snapshot) RetentionFor(alias string) config.Retention {
	r := s.defaults
	p := s.byAlias[alias]
	if p == nil || p.Retention == nil {
		return r
	}
	apply := func(dst *config.RetentionClass, o *config.RetentionClassOverride) {
		if o == nil {
			return
		}
		if o.RawDays != nil {
			dst.RawDays = *o.RawDays
		}
		if o.AggregateDays != nil {
			dst.AggregateDays = *o.AggregateDays
		}
	}
	apply(&r.Web, p.Retention.Web)
	apply(&r.Product, p.Retention.Product)
	apply(&r.App, p.Retention.App)
	return r
}

func (s *Snapshot) AggregationFor(alias string) store.ProductAggSettings {
	p := s.byAlias[alias]
	if p == nil || p.Aggregation == nil {
		return store.ProductAggSettings{}
	}
	return store.ProductAggSettings{
		Enabled:    p.Aggregation.Enabled,
		Attributes: p.Aggregation.Attributes,
		TopN:       p.Aggregation.TopN,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manage/ -v`
Expected: PASS (the poll test manipulates `lastCheck` directly, which is why it lives in the same package).

- [ ] **Step 5: Commit**

```bash
git add internal/manage/
git commit -m "feat(manage): registry snapshot with atomic swap and version poll"
```

### Task 3: `internal/manage` — operations

**Files:**
- Create: `internal/manage/ops.go`
- Create: `internal/manage/ops_test.go`

**Interfaces:**
- Consumes: Task 1 store methods, Task 2 `Registry.Reload`.
- Produces (used by CLI Tasks 9–10 and MCP Task 19):

```go
type Ops struct { /* Reg *Registry, St store.Store */ }
func NewOps(reg *Registry, st store.Store) *Ops
type ProjectSpec struct {
    Alias, Name, Identity string
    AllowedOrigins        []string
    Retention             *config.RetentionOverride
    Aggregation           *config.ProductAggregation
}
func (o *Ops) CreateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error)
func (o *Ops) UpdateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) // full replace by alias
func (o *Ops) ArchiveProject(ctx context.Context, actor, alias string) error
func (o *Ops) RestoreProject(ctx context.Context, actor, alias string) error
func (o *Ops) IssueIngestKey(ctx context.Context, actor, project, label string) (key string, err error)
func (o *Ops) DisableIngestKey(ctx context.Context, actor, project, label string) error
func (o *Ops) EnableIngestKey(ctx context.Context, actor, project, label string) error
func MintIngestKey() (string, error)  // "ak_" + 16 random bytes hex
func MintMCPToken() (string, error)   // "ar_" + 32 random bytes hex
func Snippet(origin, key, identity string) string  // the paste-ready <script> block
```

- [ ] **Step 1: Write the failing tests**

`internal/manage/ops_test.go`:

```go
package manage

import (
	"context"
	"strings"
	"testing"
)

func TestCreateProjectValidatesAndReloads(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ctx := context.Background()

	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "anonymous",
		AllowedOrigins: []string{"https://blog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Alias != "blog" {
		t.Fatalf("created = %+v", p)
	}
	// snapshot rebuilt synchronously after an in-process write
	if reg.Snapshot(ctx).Project("blog") == nil {
		t.Fatal("snapshot not reloaded")
	}
	// validation
	for _, bad := range []ProjectSpec{
		{Alias: "", Name: "x", Identity: "anonymous"},
		{Alias: "x", Name: "x", Identity: "sometimes"},
		{Alias: "x", Name: "x", Identity: "anonymous", AllowedOrigins: []string{""}},
	} {
		if _, err := ops.CreateProject(ctx, "cli", bad); err == nil {
			t.Errorf("spec %+v did not fail", bad)
		}
	}
}

func TestIssueKeyMintsAndResolves(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "b", Identity: "anonymous"}); err != nil {
		t.Fatal(err)
	}
	key, err := ops.IssueIngestKey(ctx, "mcp", "blog", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ak_") || len(key) != 3+32 {
		t.Fatalf("key = %q", key)
	}
	if p, label, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok || p.Alias != "blog" || label != "web" {
		t.Fatalf("minted key does not resolve: %v %q %v", p, label, ok)
	}
	if err := ops.DisableIngestKey(ctx, "mcp", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); ok {
		t.Fatal("disabled key still resolves")
	}
	if err := ops.EnableIngestKey(ctx, "mcp", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(key); !ok {
		t.Fatal("re-enabled key does not resolve")
	}
	// duplicate label on the same project is rejected (retire-by-label
	// depends on label uniqueness within a project)
	if _, err := ops.IssueIngestKey(ctx, "mcp", "blog", "web"); err == nil {
		t.Fatal("duplicate label did not fail")
	}
}

func TestMintersAndSnippet(t *testing.T) {
	tok, err := MintMCPToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "ar_") || len(tok) != 3+64 {
		t.Fatalf("token = %q", tok)
	}
	snip := Snippet("https://blog.example.com", "ak_x", "anonymous")
	for _, want := range []string{"script.js", `data-key="ak_x"`, `data-identity="anonymous"`} {
		if !strings.Contains(snip, want) {
			t.Errorf("snippet missing %q:\n%s", want, snip)
		}
	}
}
```

Add the tiny helper to `registry_test.go`:

```go
func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

(add `"io"` to its imports).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/manage/ -run 'TestCreate|TestIssue|TestMinters' -v`
Expected: FAIL — `NewOps` undefined.

- [ ] **Step 3: Implement `internal/manage/ops.go`**

```go
package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

// Ops are the audited registry operations. Every mutation writes its
// audit row and bumps config_version in one transaction (store layer),
// then rebuilds the snapshot synchronously so in-process readers see it
// immediately (spec §3.3).
type Ops struct {
	Reg *Registry
	St  store.Store
}

func NewOps(reg *Registry, st store.Store) *Ops { return &Ops{Reg: reg, St: st} }

type ProjectSpec struct {
	Alias, Name, Identity string
	AllowedOrigins        []string
	Retention             *config.RetentionOverride
	Aggregation           *config.ProductAggregation
}

func (sp *ProjectSpec) validate() error {
	if sp.Alias == "" {
		return fmt.Errorf("project alias must not be empty")
	}
	if sp.Name == "" {
		sp.Name = sp.Alias
	}
	if sp.Identity == "" {
		sp.Identity = config.IdentityAnonymous
	}
	switch sp.Identity {
	case config.IdentityAnonymous, config.IdentityIdentified:
	default:
		return fmt.Errorf("identity must be %q or %q, got %q",
			config.IdentityAnonymous, config.IdentityIdentified, sp.Identity)
	}
	for _, o := range sp.AllowedOrigins {
		if o == "" {
			return fmt.Errorf("allowed_origins must not contain an empty origin")
		}
	}
	return nil
}

func (sp *ProjectSpec) row() (store.RegistryProject, error) {
	origins, err := json.Marshal(sp.AllowedOrigins)
	if sp.AllowedOrigins == nil {
		origins, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	row := store.RegistryProject{Alias: sp.Alias, Name: sp.Name,
		Identity: sp.Identity, AllowedOrigins: string(origins)}
	if sp.Retention != nil {
		b, err := json.Marshal(sp.Retention)
		if err != nil {
			return row, err
		}
		row.Retention = string(b)
	}
	if sp.Aggregation != nil {
		b, err := json.Marshal(sp.Aggregation)
		if err != nil {
			return row, err
		}
		row.Aggregation = string(b)
	}
	return row, nil
}

func (o *Ops) CreateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.CreateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.create", Subject: spec.Alias}); err != nil {
		return nil, err
	}
	if err := o.Reg.Reload(ctx); err != nil {
		return nil, err
	}
	return o.Reg.Snapshot(ctx).Project(spec.Alias), nil
}

func (o *Ops) UpdateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.UpdateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.update", Subject: spec.Alias}); err != nil {
		return nil, err
	}
	if err := o.Reg.Reload(ctx); err != nil {
		return nil, err
	}
	return o.Reg.Snapshot(ctx).Project(spec.Alias), nil
}

func (o *Ops) ArchiveProject(ctx context.Context, actor, alias string) error {
	if err := o.St.SetProjectArchived(ctx, alias, true, store.AuditEntry{
		Actor: actor, Action: "project.archive", Subject: alias}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) RestoreProject(ctx context.Context, actor, alias string) error {
	if err := o.St.SetProjectArchived(ctx, alias, false, store.AuditEntry{
		Actor: actor, Action: "project.restore", Subject: alias}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) IssueIngestKey(ctx context.Context, actor, project, label string) (string, error) {
	s := o.Reg.Snapshot(ctx)
	p := s.Project(project)
	if p == nil {
		return "", fmt.Errorf("unknown project %q", project)
	}
	key, err := MintIngestKey()
	if err != nil {
		return "", err
	}
	if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
		Key: key, Project: project, Label: label}, store.AuditEntry{
		Actor: actor, Action: "key.issue", Subject: project + "/" + label}); err != nil {
		return "", err
	}
	return key, o.Reg.Reload(ctx)
}

func (o *Ops) DisableIngestKey(ctx context.Context, actor, project, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, project, label, true, store.AuditEntry{
		Actor: actor, Action: "key.disable", Subject: project + "/" + label}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) EnableIngestKey(ctx context.Context, actor, project, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, project, label, false, store.AuditEntry{
		Actor: actor, Action: "key.enable", Subject: project + "/" + label}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

// MintIngestKey mints "ak_" + 128 bits hex. Ingest keys are public by
// design (they ship in page source); 128 bits makes guessing infeasible.
func MintIngestKey() (string, error) { return mint("ak_", 16) }

// MintMCPToken mints "ar_" + 256 bits hex. Unlike ingest keys this is a
// true secret: it reads every project and authorizes management.
func MintMCPToken() (string, error) { return mint("ar_", 32) }

func mint(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entropy: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// Snippet renders the paste-ready embed tag returned by create_project,
// issue_ingest_key and `analytics key issue`.
func Snippet(origin, key, identity string) string {
	if origin == "" {
		origin = "https://analytics.example.com"
	}
	return fmt.Sprintf(`<script defer src="%s/js/script.js"
        data-key=%q
        data-identity=%q></script>`, origin, key, identity)
}
```

Note on duplicate labels: `TestIssueKeyMintsAndResolves` requires label uniqueness per project. Enforce it in `InsertIngestKey` (Task 1's file) by adding to the INSERT's error path — add this statement before the INSERT in `registry.go`:

```go
var c int
if err := tx.QueryRowContext(ctx,
	`SELECT COUNT(*) FROM ingest_keys WHERE project=? AND label=?`,
	k.Project, k.Label).Scan(&c); err != nil {
	return err
}
if c > 0 {
	return fmt.Errorf("key label %q already exists for project %q", k.Label, k.Project)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manage/ ./internal/store/sqlite/ -v`
Expected: PASS, including the Task 1 tests that still exercise `InsertIngestKey`.

- [ ] **Step 5: Commit**

```bash
git add internal/manage/ internal/store/sqlite/registry.go
git commit -m "feat(manage): audited registry operations and key minting"
```

### Task 4: Import / export

**Files:**
- Create: `internal/manage/importexport.go`
- Create: `internal/manage/importexport_test.go`

**Interfaces:**
- Consumes: Task 3 `Ops`, `config.ParseProjects` + `config.Project` (the legacy file format, kept in `internal/config` exactly for this).
- Produces:

```go
type exportDoc struct {
    Version  int             `json:"version"`   // always 1
    Projects []exportProject `json:"projects"`
}
type exportProject struct {
    Alias              string                     `json:"alias"`
    Name               string                     `json:"name"`
    Identity           string                     `json:"identity"`
    AllowedOrigins     []string                   `json:"allowed_origins"`
    Retention          *config.RetentionOverride  `json:"retention,omitempty"`
    ProductAggregation *config.ProductAggregation `json:"product_aggregation,omitempty"`
    Archived           bool                       `json:"archived,omitempty"`
    IngestKeys         []exportKey                `json:"ingest_keys"`
}
type exportKey struct {
    Key      string `json:"key"`
    Label    string `json:"label"`
    Disabled bool   `json:"disabled,omitempty"`
}
func (o *Ops) Export(ctx context.Context, w io.Writer) error
func (o *Ops) Import(ctx context.Context, actor string, r io.Reader) (ImportResult, error)
type ImportResult struct{ Created, Updated, KeysAdded int }
```

- [ ] **Step 1: Write the failing tests**

`internal/manage/importexport_test.go`:

```go
package manage

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExportImportRoundTrip(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{
		Alias: "blog", Name: "My blog", Identity: "identified",
		AllowedOrigins: []string{"https://blog.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ops.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	exported := buf.String()

	// import into a fresh database
	st2 := testStore(t)
	reg2 := New(st2, defaults, discard())
	reg2.Reload(ctx)
	ops2 := NewOps(reg2, st2)
	res, err := ops2.Import(ctx, "cli", strings.NewReader(exported))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.KeysAdded != 1 {
		t.Fatalf("result = %+v", res)
	}
	var buf2 bytes.Buffer
	if err := ops2.Export(ctx, &buf2); err != nil {
		t.Fatal(err)
	}
	if buf2.String() != exported {
		t.Errorf("round trip changed the document:\n%s\nvs\n%s", exported, buf2.String())
	}
}

func TestImportNeverArchivesOrDisables(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	for _, alias := range []string{"keep", "listed"} {
		if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: alias, Name: alias, Identity: "anonymous"}); err != nil {
			t.Fatal(err)
		}
	}
	// a document naming only "listed" must leave "keep" untouched
	doc := `{"version":1,"projects":[{"alias":"listed","name":"renamed","identity":"anonymous","allowed_origins":[],"ingest_keys":[]}]}`
	if _, err := ops.Import(ctx, "cli", strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	s := reg.Snapshot(ctx)
	if p := s.Project("keep"); p == nil || p.Archived {
		t.Fatalf("keep = %+v; import must never archive the unlisted", p)
	}
	if p := s.Project("listed"); p.Name != "renamed" {
		t.Fatalf("listed = %+v; listed fields must update", p)
	}
}

func TestImportLegacyProjectsJSON(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	reg.Reload(ctx)
	ops := NewOps(reg, st)
	// the shape projects.example.json documents today: a bare array
	legacy := `[{"alias":"blog","name":"My blog","identity":"anonymous",
	  "ingest_keys":[{"key":"ak_legacy1","label":"web"}],
	  "allowed_origins":["https://blog.example.com"]}]`
	res, err := ops.Import(ctx, "cli", strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.KeysAdded != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey("ak_legacy1"); !ok {
		t.Fatal("legacy key not imported")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/manage/ -run 'TestExport|TestImport' -v`
Expected: FAIL — `Export` undefined.

- [ ] **Step 3: Implement `internal/manage/importexport.go`**

```go
package manage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

// Export writes the full registry as one JSON document that round-trips
// through Import losslessly (spec §8). Deterministic: projects and keys
// come back alias-sorted from LoadRegistry.
func (o *Ops) Export(ctx context.Context, w io.Writer) error {
	ps, ks, err := o.St.LoadRegistry(ctx)
	if err != nil {
		return err
	}
	keysByProject := map[string][]exportKey{}
	for _, k := range ks {
		keysByProject[k.Project] = append(keysByProject[k.Project],
			exportKey{Key: k.Key, Label: k.Label, Disabled: k.Disabled})
	}
	doc := exportDoc{Version: 1}
	for _, rp := range ps {
		ep := exportProject{Alias: rp.Alias, Name: rp.Name, Identity: rp.Identity,
			Archived: rp.Archived, AllowedOrigins: []string{},
			IngestKeys: keysByProject[rp.Alias]}
		if ep.IngestKeys == nil {
			ep.IngestKeys = []exportKey{}
		}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &ep.AllowedOrigins); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		if rp.Retention != "" {
			ep.Retention = new(config.RetentionOverride)
			if err := json.Unmarshal([]byte(rp.Retention), ep.Retention); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		if rp.Aggregation != "" {
			ep.ProductAggregation = new(config.ProductAggregation)
			if err := json.Unmarshal([]byte(rp.Aggregation), ep.ProductAggregation); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		doc.Projects = append(doc.Projects, ep)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// Import applies a document declaratively: create what is missing, update
// what is listed, and NEVER archive, disable or delete what is absent
// (spec §8 — the old SyncProjects boot-archiving is not rebuilt here).
// It accepts both the v1 export document and the legacy projects.json
// bare-array format, detected by first non-space byte.
func (o *Ops) Import(ctx context.Context, actor string, r io.Reader) (ImportResult, error) {
	var res ImportResult
	br := bufio.NewReader(r)
	first, err := firstNonSpace(br)
	if err != nil {
		return res, fmt.Errorf("import: %w", err)
	}
	var projects []exportProject
	if first == '[' {
		legacy, err := config.ParseProjects(br)
		if err != nil {
			return res, fmt.Errorf("import legacy projects.json: %w", err)
		}
		for _, lp := range legacy {
			ep := exportProject{Alias: lp.Alias, Name: lp.Name, Identity: lp.Identity,
				AllowedOrigins: lp.AllowedOrigins, Retention: lp.Retention,
				ProductAggregation: lp.ProductAggregation}
			for _, k := range lp.IngestKeys {
				ep.IngestKeys = append(ep.IngestKeys, exportKey{Key: k.Key, Label: k.Label, Disabled: k.Disabled})
			}
			projects = append(projects, ep)
		}
	} else {
		var doc exportDoc
		dec := json.NewDecoder(br)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return res, fmt.Errorf("import: %w", err)
		}
		if doc.Version != 1 {
			return res, fmt.Errorf("import: unsupported version %d", doc.Version)
		}
		projects = doc.Projects
	}

	for _, ep := range projects {
		spec := ProjectSpec{Alias: ep.Alias, Name: ep.Name, Identity: ep.Identity,
			AllowedOrigins: ep.AllowedOrigins, Retention: ep.Retention,
			Aggregation: ep.ProductAggregation}
		// snapshot re-read per iteration: CreateProject reloads it, and a
		// document may (erroneously) repeat an alias — the second pass
		// must see the first.
		if o.Reg.Snapshot(ctx).Project(ep.Alias) == nil {
			if _, err := o.CreateProject(ctx, actor, spec); err != nil {
				return res, fmt.Errorf("import %q: %w", ep.Alias, err)
			}
			res.Created++
		} else {
			if _, err := o.UpdateProject(ctx, actor, spec); err != nil {
				return res, fmt.Errorf("import %q: %w", ep.Alias, err)
			}
			res.Updated++
		}
		existing := map[string]bool{}
		_, ks, err := o.St.LoadRegistry(ctx)
		if err != nil {
			return res, err
		}
		for _, k := range ks {
			existing[k.Key] = true
		}
		for _, ek := range ep.IngestKeys {
			if ek.Key == "" || existing[ek.Key] {
				continue // present keys are left as they are; explicit only
			}
			if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
				Key: ek.Key, Project: ep.Alias, Label: ek.Label},
				store.AuditEntry{Actor: actor, Action: "key.import",
					Subject: ep.Alias + "/" + ek.Label}); err != nil {
				return res, fmt.Errorf("import key %q/%q: %w", ep.Alias, ek.Label, err)
			}
			if ek.Disabled {
				if err := o.St.SetIngestKeyDisabled(ctx, ep.Alias, ek.Label, true,
					store.AuditEntry{Actor: actor, Action: "key.disable",
						Subject: ep.Alias + "/" + ek.Label}); err != nil {
					return res, err
				}
			}
			res.KeysAdded++
		}
	}
	return res, o.Reg.Reload(ctx)
}

func firstNonSpace(br *bufio.Reader) (byte, error) {
	for {
		bs, err := br.Peek(1)
		if err != nil {
			return 0, err
		}
		switch bs[0] {
		case ' ', '\t', '\n', '\r':
			br.ReadByte()
		default:
			return bs[0], nil
		}
	}
}

type ImportResult struct{ Created, Updated, KeysAdded int }

type exportDoc struct {
	Version  int             `json:"version"`
	Projects []exportProject `json:"projects"`
}

type exportProject struct {
	Alias              string                     `json:"alias"`
	Name               string                     `json:"name"`
	Identity           string                     `json:"identity"`
	AllowedOrigins     []string                   `json:"allowed_origins"`
	Retention          *config.RetentionOverride  `json:"retention,omitempty"`
	ProductAggregation *config.ProductAggregation `json:"product_aggregation,omitempty"`
	Archived           bool                       `json:"archived,omitempty"`
	IngestKeys         []exportKey                `json:"ingest_keys"`
}

type exportKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manage/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/manage/importexport.go internal/manage/importexport_test.go
git commit -m "feat(manage): never-destructive JSON import/export with legacy projects.json support"
```

### Task 5: DeleteProject cascade

**Files:**
- Modify: `internal/store/store.go` (add `DeleteProjectData` to the interface)
- Modify: `internal/store/sqlite/registry.go`
- Modify: `internal/manage/ops.go`
- Test: `internal/store/sqlite/registry_test.go`, `internal/manage/ops_test.go`

**Interfaces:**
- Produces: `DeleteProjectData(ctx, alias string, a store.AuditEntry) error` on the Store interface; `(o *Ops) DeleteProject(ctx context.Context, actor, alias string) error` (CLI-only frontend exposure; NOT registered as an MCP tool — spec §7.3).

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite/registry_test.go`:

```go
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
			`SELECT COUNT(*) FROM `+table+` WHERE `+projectCol(table)+`='blog'`).Scan(&c); err != nil {
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
```

with the tiny helper at the bottom of the test file:

```go
func projectCol(table string) string {
	if table == "projects" {
		return "alias"
	}
	return "project"
}
```

Check the web_hits column list against `internal/store/sqlite/migrations/001_init.sql` before finalizing the INSERT — the migration is the source of truth, not this plan.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestDeleteProjectData -v`
Expected: FAIL — `DeleteProjectData` undefined.

- [ ] **Step 3: Implement**

Append to `internal/store/sqlite/registry.go`:

```go
// projectTables is every table carrying a per-project `project` column.
// Kept in one place so a future migration adding a table has one list to
// extend; the delete test cross-checks the count against sqlite_master.
var projectTables = []string{
	"web_hits", "product_events", "app_views",
	"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
	"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	"agg_app_daily", "agg_app_screens", "agg_app_versions", "agg_app_os",
	"agg_app_devices", "agg_app_countries",
	"actors", "agg_retention", "identities", "agg_identity_daily",
	"ingest_keys",
}

// DeleteProjectData hard-deletes the project and every row keyed by its
// alias, in one transaction (spec §7.3). The audit row is written in the
// same transaction and survives — audit_log has no project column.
// Page reclamation is the caller's job (IncrementalVacuum), because a
// vacuum inside the tx would deadlock the single connection.
func (d *DB) DeleteProjectData(ctx context.Context, alias string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE alias=?`, alias)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("delete: unknown alias %q", alias)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE project=?`, alias); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}
```

Append to `internal/manage/ops.go`:

```go
// DeleteProject is exposed by the CLI only — never as an MCP tool
// (spec §7.3: irreversible operations require a shell). Reclaims pages
// afterwards; the tx cannot (single connection).
func (o *Ops) DeleteProject(ctx context.Context, actor, alias string) error {
	if err := o.St.DeleteProjectData(ctx, alias, store.AuditEntry{
		Actor: actor, Action: "project.delete", Subject: alias}); err != nil {
		return err
	}
	if err := o.St.IncrementalVacuum(ctx); err != nil {
		return fmt.Errorf("delete succeeded but vacuum failed: %w", err)
	}
	return o.Reg.Reload(ctx)
}
```

Add `DeleteProjectData(ctx context.Context, alias string, a AuditEntry) error` to the `Store` interface in `internal/store/store.go`.

- [ ] **Step 4: Run tests, then the full package**

Run: `go test ./internal/store/sqlite/ ./internal/manage/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/ internal/manage/
git commit -m "feat(manage): CLI-only hard delete with full cascade"
```

### Task 6: Config surgery — `PROJECTS_FILE` retired

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`, `internal/config/example_config_test.go` (delete project-loading cases; keep infra cases)

**Interfaces:**
- Produces: `config.Load()` / `config.FromEnv(lookup)` return infra-only config — **no** `Projects` field, no `keys`, no `ProjectByKey`, no `RetentionFor`, no `Project()`, no `DisabledKeyProjects()`. `config.Project`, `config.IngestKey`, `config.ParseProjects` **stay** (they are the legacy-import format used by Task 4). `config.Retention*`, `config.ProductAggregation`, `MaxEventAge()` stay.
- Consumers break in this task and are fixed in Tasks 7–8; commit at the end of Task 8 if the tree cannot build earlier — or (preferred) do Tasks 6–8 as one branch of commits where only the final one must pass `make check`. Each task still runs its own package tests.

- [ ] **Step 1: Adjust the config tests**

In `internal/config/config_test.go` delete every test that writes a projects file or asserts on `cfg.Projects`, `ProjectByKey`, `RetentionFor`, `validate`'s project cases, `DisabledKeyProjects`. Keep/adjust: env parsing (str/num/dur errors), DATABASE_URL required, DSN validation, retention negatives, `MaxEventAge`. Add:

```go
func TestLoadDoesNotRequireProjectsFile(t *testing.T) {
	cfg, err := FromEnv(func(k string) (string, bool) {
		if k == "DATABASE_URL" {
			return "sqlite:///tmp/x.db", true
		}
		return "", false // PROJECTS_FILE unset, no file anywhere
	})
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Database != "sqlite:///tmp/x.db" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
```

- [ ] **Step 2: Run to verify the new test fails**

Run: `go test ./internal/config/ -run TestLoadDoesNotRequire -v`
Expected: FAIL — `FromEnv` still tries to open the projects file.

- [ ] **Step 3: Perform the surgery on `internal/config/config.go`**

- Delete: `Config.Projects`, `Config.keys`, `keyOwner`, `DefaultProjectsFile`, `applyDefaults()`, the project half of `validate()` (keep DATABASE_URL/DSN/retention checks), `Project()`, `RetentionFor()`, `ProjectByKey()`, `DisabledKeyProjects()`.
- Collapse `parse(lookup, withProjects)` to `parse(lookup)`; `FromEnv` and `FromEnvDashboards` differ only in the DASHBOARDS_DB_PATH defaulting, which moves behind a boolean parameter or stays as a small wrapper — keep the two exported functions with identical signatures.
- Keep `Project`, `IngestKey`, `ParseProjects` with a doc comment: "Legacy projects.json format, used only by `analytics config import`."
- Update the package doc comment: config no longer reads any file.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/config/ -v`
Expected: PASS. `go build ./...` FAILS (server/jobs/app still reference removed methods) — expected until Task 8.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "refactor(config): retire PROJECTS_FILE; keep legacy format for import"
```

### Task 7: Server reads the Snapshot

**Files:**
- Modify: `internal/server/server.go`, `internal/server/handlers.go`
- Modify: `internal/server/server_test.go`, `internal/server/ingest_test.go`

**Interfaces:**
- Consumes: `manage.Registry` / `manage.Snapshot` (Task 2).
- Produces: `server.New(cfg *config.Config, reg *manage.Registry, q Enqueuer, g geo.Provider, salt Salt, names NameStore, logger *slog.Logger) *Server` — note the added `reg` parameter; Task 8's `app.Serve` and Phase B use this signature.

- [ ] **Step 1: Update the server**

In `server.go`:
- `Server` struct: replace `originOK map[string]map[string]bool` with `reg *manage.Registry`; keep `cfg` (still used for `MaxEventAge`).
- `New`: drop the originOK loop; store `reg`.
- `originAllowed`: body becomes

```go
func (s *Server) originAllowed(w http.ResponseWriter, r *http.Request, project string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !s.reg.Snapshot(r.Context()).OriginAllowed(project, origin) {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	return true
}
```

- `anyOriginAllowed(origin)` → `s.reg.Snapshot(ctx).AnyOriginAllowed(origin)` (pass `r.Context()` from `handlePreflight`).

In `handlers.go`:
- `s.cfg.ProjectByKey(key)` → `s.reg.Snapshot(r.Context()).ProjectByKey(key)`.
- `resolveIdentity(p *config.Project, …)` → `resolveIdentity(p *manage.Project, …)`; same for `identityNames`. The bodies only touch `p.Identity` — the type swap is mechanical.

- [ ] **Step 2: Update the server tests**

The tests currently build a `*config.Config` with inline projects. Add one helper and switch every construction site to it:

```go
// newTestRegistry seeds a temp-DB registry with the given projects.
// Replaces the old inline cfg.Projects construction.
func newTestRegistry(t *testing.T, cfg *config.Config, projects []manage.ProjectSpec, keys map[string][2]string) *manage.Registry {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/reg.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, cfg.Retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	for _, spec := range projects {
		if _, err := ops.CreateProject(ctx, "test", spec); err != nil {
			t.Fatal(err)
		}
	}
	for project, kl := range keys { // project -> {key, label}
		if err := st.InsertIngestKey(ctx, store.RegistryKey{
			Key: kl[0], Project: project, Label: kl[1]},
			store.AuditEntry{Actor: "test", Action: "key.issue", Subject: kl[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return reg
}
```

(`InsertIngestKey` directly rather than `IssueIngestKey` so tests keep their fixed `ak_test…` key strings.) Update every `server.New(...)` call to pass the registry.

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/server/ -v`
Expected: PASS with unchanged test semantics — same accepted/rejected counts, same CORS behaviour, same identity resolution.

- [ ] **Step 4: Commit**

```bash
git add internal/server/
git commit -m "refactor(server): resolve keys, origins and identity from the registry snapshot"
```

### Task 8: Jobs + app wiring; `SyncProjects` deleted

**Files:**
- Modify: `internal/jobs/jobs.go`, `internal/jobs/jobs_test.go`
- Modify: `internal/app/app.go`, `internal/app/app_test.go`
- Modify: `internal/store/store.go`, `internal/store/sqlite/write.go`, `internal/store/sqlite/write_test.go` (remove `SyncProjects`, `ProjectInfo`)
- Modify: `cmd/analytics/serve.go` is untouched here (flags come in Task 21); `cmd/analytics/migrate.go` compiles unchanged because `config.Load` kept its signature.

**Interfaces:**
- Produces: `jobs.New(st store.Store, cfg *config.Config, reg *manage.Registry, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner`.

- [ ] **Step 1: Update jobs**

In `RunDailyPass`:
- Delete the "Config projects may not be synced yet" union block — the DB is the truth now; `store.ProjectAliases` is the complete list.
- `ret := r.cfg.RetentionFor(id)` → `snap := r.reg.Snapshot(ctx)` once before the loop; `ret := snap.RetentionFor(id)`.
- The `identified` check: `p := snap.Project(id); identified := p != nil && p.Identity == config.IdentityIdentified`.
- `aggSettingsFor(r.cfg.Project(id))` → `snap.AggregationFor(id)` (delete `aggSettingsFor`).
- Behaviour change to document in the code comment: an archived project now keeps its retention overrides (it is still a registry row), where the old config-absence fallback silently reverted it to global defaults.

- [ ] **Step 2: Update jobs tests**

`jobs_test.go` builds configs with projects; switch to `newTestRegistry`-style seeding against the same store the runner uses (jobs tests already open a real store — seed registry rows into it directly with `st.CreateProject`). Identified-mode cases set `Identity: "identified"` on the seeded row.

- [ ] **Step 3: Update app**

In `app.Serve`:
- Delete the `store.ProjectInfo` loop and the `st.SyncProjects` call.
- After `st.Migrate`: build the registry —

```go
reg := manage.New(st, cfg.Retention, logger)
if err := reg.Reload(ctx); err != nil {
	return err
}
if len(reg.Snapshot(ctx).Projects()) == 0 {
	logger.Warn("no projects configured; create one with `analytics project create` or an MCP management tool")
}
if legacy := os.Getenv("PROJECTS_FILE"); legacy != "" {
	logger.Warn("PROJECTS_FILE is no longer read; import it once with `analytics config import`", "file", legacy)
} else if _, err := os.Stat("/etc/analytics/projects.json"); err == nil {
	logger.Warn("projects.json found but no longer read; import it once with `analytics config import /etc/analytics/projects.json`")
}
```

- Replace the `DisabledKeyProjects` warning loop with a snapshot-based one:

```go
for _, p := range reg.Snapshot(ctx).Projects() {
	if p.Archived {
		continue
	}
	if _, _, ok := reg.Snapshot(ctx).ProjectByKey(""); ok {
		_ = ok // placeholder-free: see below
	}
}
```

**No — the above is wrong; do this instead.** Add to `Snapshot`:

```go
// KeylessProjects lists active projects with no active key: a legitimate
// retired state, so callers warn rather than fail.
func (s *Snapshot) KeylessProjects() []string {
	withKey := map[string]bool{}
	for _, k := range s.keys {
		withKey[k.project.Alias] = true
	}
	var out []string
	for _, p := range s.ordered {
		if !p.Archived && !withKey[p.Alias] {
			out = append(out, p.Alias)
		}
	}
	return out
}
```

and in `app.Serve`:

```go
for _, alias := range reg.Snapshot(ctx).KeylessProjects() {
	logger.Warn("project has no active ingest keys and can receive nothing", "project", alias)
}
```

- `server.New(cfg, buf, …)` → `server.New(cfg, reg, buf, …)`; `jobs.New(st, cfg, salter, …)` → `jobs.New(st, cfg, reg, salter, …)`; log line `"projects", len(cfg.Projects)` → `"projects", len(reg.Snapshot(ctx).Projects())`.

- [ ] **Step 4: Delete `SyncProjects`**

Remove `SyncProjects` and `ProjectInfo` from `internal/store/store.go`, the implementation from `write.go`, and its tests from `write_test.go`. `ProjectAliases` stays.

Add one test to `internal/app/app_test.go` for the legacy-file warning (spec §12): construct the logger over a `strings.Builder`, run `Serve` briefly with `PROJECTS_FILE` set via `t.Setenv` to an existing temp file, cancel, and assert the log contains `analytics config import`; a second run with the variable unset and no `/etc/analytics/projects.json` (the default path is absent in CI anyway) must not contain it.

- [ ] **Step 5: Run everything**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: PASS across the tree — this is the first commit after the surgery where the whole tree must be green.

- [ ] **Step 6: Commit**

```bash
git add internal/ cmd/
git commit -m "refactor: registry is the sole source of project config; SyncProjects deleted"
```

### Task 9: CLI — `analytics project`

**Files:**
- Create: `cmd/analytics/project.go`
- Create: `cmd/analytics/project_test.go`
- Modify: `cmd/analytics/main.go` (usage line)

**Interfaces:**
- Consumes: `manage.NewOps`, `config.Load`, `store.Open`. Actor string is `"cli"`.
- Produces: `analytics project create|update|list|archive|restore|delete` with flags per spec §7.1; shared helper `openOps(stdout io.Writer, envFile string) (*manage.Ops, func(), int)` reused by Tasks 10–11.

- [ ] **Step 1: Write the failing test**

`cmd/analytics/project_test.go` (the cmd package tests already run commands via `run(args, stdout)` — follow `commands_test.go`):

```go
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// withDB points DATABASE_URL at a fresh temp file for one test.
func withDB(t *testing.T) string {
	t.Helper()
	dsn := "sqlite://" + t.TempDir() + "/cli.db"
	t.Setenv("DATABASE_URL", dsn)
	return dsn
}

func TestProjectCreateListArchiveDelete(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-alias", "blog", "-name", "My blog",
		"-origin", "https://blog.example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(out.String(), "blog") {
		t.Fatalf("list output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "archive", "-alias", "blog"}, &out); code != 0 {
		t.Fatalf("archive: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "restore", "-alias", "blog"}, &out); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, out.String())
	}
	// delete refuses without -force
	out.Reset()
	if code := run([]string{"project", "delete", "-alias", "blog"}, &out); code == 0 {
		t.Fatal("delete without -force succeeded")
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-alias", "blog", "-force"}, &out); code != 0 {
		t.Fatalf("delete -force: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || strings.Contains(out.String(), "blog") {
		t.Fatalf("blog survived delete: %s", out.String())
	}
}

func TestProjectCreateUnknownSubcommand(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "frobnicate"}, &out); code != 2 {
		t.Fatalf("exit = %d", code)
	}
}

func TestEnvFileFlag(t *testing.T) {
	dir := t.TempDir()
	envFile := dir + "/analytics.env"
	os.WriteFile(envFile, []byte("DATABASE_URL=sqlite://"+dir+"/env.db\n"), 0o600)
	os.Unsetenv("DATABASE_URL")
	var out bytes.Buffer
	if code := run([]string{"project", "-env-file", envFile, "create", "-alias", "x"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/analytics/ -run TestProject -v`
Expected: FAIL — `unknown command "project"`.

- [ ] **Step 3: Implement `cmd/analytics/project.go`**

```go
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmitry/analytics/internal/app"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func init() { commands["project"] = cmdProject }

// envFileLookup overlays KEY=VALUE lines from path under the real
// environment: real env wins, matching how EnvironmentFile= behaves.
func envFileLookup(path string) (func(string) (string, bool), error) {
	fromFile := map[string]string{}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				fromFile[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := fromFile[key]
		return v, ok
	}, nil
}

// openOps opens the store named by DATABASE_URL (optionally via
// -env-file), migrates, and returns the management frontend. The CLI
// talks to the database directly — break-glass by design (spec §7.1).
func openOps(stdout io.Writer, envFile string) (*manage.Ops, func(), int) {
	lookup, err := envFileLookup(envFile)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, 1
	}
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, 1
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return nil, nil, 1
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, 1
	}
	reg := manage.New(st, cfg.Retention, app.NewLogger(cfg.Log))
	if err := reg.Reload(ctx); err != nil {
		st.Close()
		fmt.Fprintln(stdout, err)
		return nil, nil, 1
	}
	return manage.NewOps(reg, st), func() { st.Close() }, 0
}

func cmdProject(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: analytics project <create|update|list|archive|restore|delete> [flags]")
		return 2
	}
	ops, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "create", "update":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		name := sf.String("name", "", "display name (defaults to alias)")
		identity := sf.String("identity", "anonymous", "anonymous|identified")
		var origins multiFlag
		sf.Var(&origins, "origin", "allowed origin (repeatable)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		spec := manage.ProjectSpec{Alias: *alias, Name: *name,
			Identity: *identity, AllowedOrigins: origins}
		var p *manage.Project
		var err error
		if sub == "create" {
			p, err = ops.CreateProject(ctx, "cli", spec)
		} else {
			p, err = ops.UpdateProject(ctx, "cli", spec)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q %sd\n", p.Alias, sub)
		fmt.Fprintln(stdout, "next: analytics key issue -project", p.Alias, "-label web")
		return 0
	case "list":
		s := ops.Reg.Snapshot(ctx)
		for _, p := range s.Projects() {
			state := ""
			if p.Archived {
				state = "  (archived)"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s%s\n", p.Alias, p.Identity, p.Name, state)
		}
		return 0
	case "archive", "restore":
		sf := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		var err error
		if sub == "archive" {
			err = ops.ArchiveProject(ctx, "cli", *alias)
		} else {
			err = ops.RestoreProject(ctx, "cli", *alias)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q %sd\n", *alias, sub)
		return 0
	case "delete":
		sf := flag.NewFlagSet("project delete", flag.ContinueOnError)
		sf.SetOutput(stdout)
		alias := sf.String("alias", "", "project alias (required)")
		force := sf.Bool("force", false, "skip confirmation")
		if err := sf.Parse(subArgs); err != nil {
			return 2
		}
		if !*force {
			fmt.Fprintf(stdout, "This permanently deletes project %q and ALL its data.\n", *alias)
			fmt.Fprintln(stdout, "Re-run with -force to confirm.")
			return 1
		}
		if err := ops.DeleteProject(ctx, "cli", *alias); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "project %q deleted\n", *alias)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: analytics project <create|update|list|archive|restore|delete> [flags]\n", sub)
		return 2
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
```

Update the two usage strings in `cmd/analytics/main.go` to `usage: analytics <serve|dashboards|migrate|keygen|project|key|config|version> [flags]`.

Design note honored from spec §7.3: interactive typed-alias confirmation would need a TTY check; `-force` is the scriptable gate and the no-`-force` path prints what would be lost and exits 1. That satisfies "requires `-force` or confirmation" without adding TTY detection to a stdout-only command surface.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/analytics/ -v`
Expected: PASS (the `key`/`config` commands referenced in usage do not exist until Tasks 10–11 — the usage string is text, not a registration).

- [ ] **Step 5: Commit**

```bash
git add cmd/analytics/
git commit -m "feat(cli): analytics project create/update/list/archive/restore/delete"
```

### Task 10: CLI — `analytics key`, keygen `-mcp`

**Files:**
- Create: `cmd/analytics/key.go`
- Modify: `cmd/analytics/keygen.go`
- Test: `cmd/analytics/key_test.go`, extend `cmd/analytics/commands_test.go` keygen cases

**Interfaces:**
- Consumes: `openOps` (Task 9), `manage.MintMCPToken`, `manage.Snippet`.

- [ ] **Step 1: Write the failing tests**

`cmd/analytics/key_test.go`:

```go
package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestKeyIssueListDisableEnable(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-alias", "blog"}, &out); code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"key", "issue", "-project", "blog", "-label", "web"}, &out); code != 0 {
		t.Fatalf("issue: exit %d: %s", code, out.String())
	}
	if !regexp.MustCompile(`ak_[0-9a-f]{32}`).MatchString(out.String()) {
		t.Fatalf("no key in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "script.js") {
		t.Fatalf("no snippet in output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"key", "list", "-project", "blog"}, &out); code != 0 ||
		!strings.Contains(out.String(), "web") {
		t.Fatalf("list: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"key", "disable", "-project", "blog", "-label", "web"}, &out); code != 0 {
		t.Fatalf("disable: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"key", "list", "-project", "blog"}, &out); !strings.Contains(out.String(), "disabled") {
		t.Fatalf("list after disable: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"key", "enable", "-project", "blog", "-label", "web"}, &out); code != 0 {
		t.Fatalf("enable: %s", out.String())
	}
}

func TestKeygenMCP(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-mcp"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !regexp.MustCompile(`MCP_TOKEN=ar_[0-9a-f]{64}`).MatchString(out.String()) {
		t.Fatalf("output: %s", out.String())
	}
	if strings.Contains(out.String(), "ingest_keys") {
		t.Fatal("-mcp must not print the JSON block")
	}
}

func TestKeygenDeprecationNotice(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "key issue") {
		t.Fatalf("no deprecation pointer: %s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/analytics/ -run 'TestKey|TestKeygen' -v`
Expected: FAIL — `unknown command "key"`; keygen has no `-mcp`.

- [ ] **Step 3: Implement**

`cmd/analytics/key.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/manage"
)

func init() { commands["key"] = cmdKey }

func cmdKey(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: analytics key <issue|list|disable|enable> [flags]")
		return 2
	}
	ops, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	sub, subArgs := rest[0], rest[1:]
	sf := flag.NewFlagSet("key "+sub, flag.ContinueOnError)
	sf.SetOutput(stdout)
	project := sf.String("project", "", "project alias (required)")
	label := sf.String("label", "", "key label")
	if err := sf.Parse(subArgs); err != nil {
		return 2
	}
	switch sub {
	case "issue":
		key, err := ops.IssueIngestKey(ctx, "cli", *project, *label)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		p := ops.Reg.Snapshot(ctx).Project(*project)
		origin := ""
		if len(p.AllowedOrigins) > 0 {
			origin = p.AllowedOrigins[0]
		}
		fmt.Fprintf(stdout, "issued %s (label %q)\n\nWeb snippet:\n\n%s\n",
			key, *label, manage.Snippet(origin, key, p.Identity))
		return 0
	case "list":
		// list reads the raw rows so disabled keys are visible; the
		// snapshot deliberately drops them.
		_, ks, err := ops.St.LoadRegistry(ctx)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		for _, k := range ks {
			if *project != "" && k.Project != *project {
				continue
			}
			state := "active"
			if k.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", k.Project, k.Label, k.Key, state)
		}
		return 0
	case "disable", "enable":
		var err error
		if sub == "disable" {
			err = ops.DisableIngestKey(ctx, "cli", *project, *label)
		} else {
			err = ops.EnableIngestKey(ctx, "cli", *project, *label)
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "key %s/%s %sd\n", *project, *label, sub)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: analytics key <issue|list|disable|enable> [flags]\n", sub)
		return 2
	}
}
```

`cmd/analytics/keygen.go` changes: add `mcp := fs.Bool("mcp", false, "mint the MCP access token instead")`; when set, print exactly

```go
if *mcp {
	tok, err := manage.MintMCPToken()
	if err != nil {
		fmt.Fprintf(stdout, "keygen: entropy: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Add to analytics.env:")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  MCP_TOKEN=%s\n", tok)
	return 0
}
```

and prepend one line to the legacy path's output: `fmt.Fprintln(stdout, "note: prefer `analytics key issue`, which registers the key as it mints it")` — keep the rest of the legacy output as is (still useful offline).

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/analytics/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/analytics/
git commit -m "feat(cli): analytics key lifecycle; keygen -mcp mints the MCP token"
```

### Task 11: CLI — `analytics config import|export`

**Files:**
- Create: `cmd/analytics/configcmd.go`
- Test: `cmd/analytics/configcmd_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestConfigImportExportRoundTrip(t *testing.T) {
	withDB(t)
	dir := t.TempDir()
	legacy := dir + "/projects.json"
	os.WriteFile(legacy, []byte(`[{"alias":"blog","name":"My blog",
	  "ingest_keys":[{"key":"ak_legacy_cli","label":"web"}]}]`), 0o600)
	var out bytes.Buffer
	if code := run([]string{"config", "import", legacy}, &out); code != 0 {
		t.Fatalf("import: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "1 created") {
		t.Fatalf("import output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"config", "export"}, &out); code != 0 {
		t.Fatalf("export: exit %d", code)
	}
	if !strings.Contains(out.String(), `"alias": "blog"`) ||
		!strings.Contains(out.String(), "ak_legacy_cli") {
		t.Fatalf("export output: %s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/analytics/ -run TestConfigImport -v` → FAIL, unknown command.

- [ ] **Step 3: Implement `cmd/analytics/configcmd.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

func init() { commands["config"] = cmdConfig }

func cmdConfig(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stdout)
	envFile := fs.String("env-file", "", "load environment from this file (real env wins)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stdout, "usage: analytics config <import FILE|export>")
		return 2
	}
	ops, closeStore, code := openOps(stdout, *envFile)
	if code != 0 {
		return code
	}
	defer closeStore()
	ctx := context.Background()
	switch rest[0] {
	case "export":
		if err := ops.Export(ctx, stdout); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		return 0
	case "import":
		if len(rest) != 2 {
			fmt.Fprintln(stdout, "usage: analytics config import FILE")
			return 2
		}
		f, err := os.Open(rest[1])
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		defer f.Close()
		res, err := ops.Import(ctx, "cli", f)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "%d created, %d updated, %d keys added\n",
			res.Created, res.Updated, res.KeysAdded)
		return 0
	default:
		fmt.Fprintf(stdout, "unknown subcommand %q\nusage: analytics config <import FILE|export>\n", rest[0])
		return 2
	}
}
```

- [ ] **Step 4: Run** — `go test ./cmd/analytics/ -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/analytics/configcmd.go cmd/analytics/configcmd_test.go
git commit -m "feat(cli): analytics config import/export"
```

### Task 12: Phase A deploy story

**Files:**
- Modify: `.env.example`, `scripts/smoke.sh`, `scripts/test-compose.sh`, `deploy/install.sh`, `Makefile` (run target), `README.md`, `docs/deployment.md`
- Delete: `projects.example.json`

- [ ] **Step 1: `.env.example`** — delete the `PROJECTS_FILE=` line; add under it:

```
# Project configuration lives in the database. Create projects with
# `analytics project create` (or an MCP management tool); migrate an old
# projects.json once with `analytics config import`.
```

- [ ] **Step 2: `scripts/smoke.sh`** — where it writes `local/projects.json` and points `PROJECTS_FILE` at it, instead run (before starting the server):

```bash
./analytics project create -alias smoke -name "Smoke" -origin "http://localhost:9\d*" \
  || fail "project create failed"
key=$(./analytics key issue -project smoke -label smoke | grep -o 'ak_[0-9a-f]*' | head -1) \
  || fail "key issue failed"
```

and use `$key` where the script previously read the key from the JSON file. (The exact current lines differ — adapt in place; DATABASE_URL is already exported by the script.)

- [ ] **Step 3: `scripts/test-compose.sh`** — replace the projects.json mount/seed with `docker compose exec analytics analytics project create …` + `key issue`, captured the same way. Remove the `projects.json` reference from `deploy/compose/docker-compose.yml` (the volume/mount line) — check `git grep projects.json deploy/` and clear every hit.

- [ ] **Step 4: `deploy/install.sh`** — remove the `projects.example.json` install and the "edit projects.json" prompt; print instead: `Create your first project: sudo -u analytics sh -ac '. /etc/analytics/analytics.env; analytics project create -alias myapp'`.

- [ ] **Step 5: `Makefile` run target** — drop the `local/projects.json` copy; add `./$(BIN) project create -alias dev 2>/dev/null || true` before starting.

- [ ] **Step 6: README + docs/deployment.md** — Quickstarts lose the "edit projects.json" step and gain the two CLI lines; Configuration section: the projects.json table becomes the registry description + CLI examples; Embedding section: `analytics key issue` replaces `analytics keygen`; deployment.md runbook rows for import/export. Delete `projects.example.json` and every reference (`git grep -l projects.example`).

- [ ] **Step 7: Full verification**

Run: `make check && make build && ./scripts/smoke.sh`
Expected: all green — this closes Phase A.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat: registry-first deploy story; projects.json retired"
```

---

## Phase B — MCP endpoint

### Task 13: Dependencies and MCP configuration

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
type MCPConfig struct {
    Addr         string        // MCP_ADDR, defaults to Listen
    DBPath       string        // MCP_DB_PATH, defaults to DATABASE_URL path
    AuthMode     string        // "oauth" | "cloudflare" | "token"; no default
    ResourceURL  string        // MCP_RESOURCE_URL
    Issuer       string        // MCP_AUTH_ISSUER
    Audience     string        // MCP_AUTH_AUDIENCE, defaults to ResourceURL
    CFTeamDomain string        // MCP_CF_TEAM_DOMAIN
    CFAud        string        // MCP_CF_AUD
    Token        string        // MCP_TOKEN
    QueryTimeout time.Duration // MCP_QUERY_TIMEOUT, default 10s
    QueryMaxRows int           // MCP_QUERY_MAX_ROWS, default 1000
}
// on Config: MCP MCPConfig, populated by parse()
func (c *Config) ValidateMCP() error   // called only when -mcp is set
```

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/modelcontextprotocol/go-sdk@v1.7.0 github.com/golang-jwt/jwt/v5@latest
go mod tidy
```

`go.mod` gains exactly two direct requires. Note: `go mod tidy` will drop them again until Task 14 imports them — run `go get` in Task 14's commit instead if tidy complains; the plan keeps this step here so the version pin is a reviewed, single-line decision.

- [ ] **Step 2: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func mcpEnv(over map[string]string) func(string) (string, bool) {
	base := map[string]string{
		"DATABASE_URL": "sqlite:///tmp/x.db",
		"MCP_AUTH_MODE": "token", "MCP_TOKEN": "ar_x",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return func(k string) (string, bool) { v, ok := base[k]; return v, ok }
}

func TestValidateMCP(t *testing.T) {
	cases := []struct {
		name string
		over map[string]string
		ok   bool
	}{
		{"token mode ok", nil, true},
		{"no mode", map[string]string{"MCP_AUTH_MODE": ""}, false},
		{"unknown mode", map[string]string{"MCP_AUTH_MODE": "basic"}, false},
		{"token mode missing token", map[string]string{"MCP_TOKEN": ""}, false},
		{"oauth ok", map[string]string{"MCP_AUTH_MODE": "oauth", "MCP_TOKEN": "",
			"MCP_AUTH_ISSUER": "https://idp.example.com",
			"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"}, true},
		{"oauth missing issuer", map[string]string{"MCP_AUTH_MODE": "oauth", "MCP_TOKEN": "",
			"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"}, false},
		{"oauth missing resource", map[string]string{"MCP_AUTH_MODE": "oauth", "MCP_TOKEN": "",
			"MCP_AUTH_ISSUER": "https://idp.example.com"}, false},
		{"cloudflare ok", map[string]string{"MCP_AUTH_MODE": "cloudflare", "MCP_TOKEN": "",
			"MCP_CF_TEAM_DOMAIN": "team.cloudflareaccess.com", "MCP_CF_AUD": "aud123"}, true},
		{"cloudflare missing aud", map[string]string{"MCP_AUTH_MODE": "cloudflare", "MCP_TOKEN": "",
			"MCP_CF_TEAM_DOMAIN": "team.cloudflareaccess.com"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromEnv(mcpEnv(tc.over))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateMCP()
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateMCP = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestMCPDefaults(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Addr != cfg.Listen {
		t.Errorf("Addr = %q, want Listen %q", cfg.MCP.Addr, cfg.Listen)
	}
	if cfg.MCP.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.MCP.DBPath)
	}
	if cfg.MCP.QueryTimeout != 10*time.Second || cfg.MCP.QueryMaxRows != 1000 {
		t.Errorf("guards = %v %d", cfg.MCP.QueryTimeout, cfg.MCP.QueryMaxRows)
	}
}

func TestMCPAudienceDefaultsToResource(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{"MCP_AUTH_MODE": "oauth", "MCP_TOKEN": "",
		"MCP_AUTH_ISSUER": "https://idp.example.com",
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Audience != "https://analytics.example.com/mcp" {
		t.Errorf("Audience = %q", cfg.MCP.Audience)
	}
}
```

- [ ] **Step 3: Run to verify failure** — `go test ./internal/config/ -run 'TestValidateMCP|TestMCP' -v` → FAIL.

- [ ] **Step 4: Implement in `internal/config/config.go`**

In `parse()` after the Dashboards block:

```go
c.MCP = MCPConfig{
	Addr:         e.str("MCP_ADDR", c.Listen),
	DBPath:       e.str("MCP_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
	AuthMode:     e.str("MCP_AUTH_MODE", ""),
	ResourceURL:  e.str("MCP_RESOURCE_URL", ""),
	Issuer:       e.str("MCP_AUTH_ISSUER", ""),
	CFTeamDomain: e.str("MCP_CF_TEAM_DOMAIN", ""),
	CFAud:        e.str("MCP_CF_AUD", ""),
	Token:        e.str("MCP_TOKEN", ""),
	QueryTimeout: e.dur("MCP_QUERY_TIMEOUT", 10*time.Second),
	QueryMaxRows: e.num("MCP_QUERY_MAX_ROWS", 1000),
}
c.MCP.Audience = e.str("MCP_AUTH_AUDIENCE", c.MCP.ResourceURL)
```

And:

```go
// ValidateMCP fail-fasts the -mcp surface (endpoint spec §4): there is no
// unauthenticated mode and no way to reach one by omission.
func (c *Config) ValidateMCP() error {
	m := c.MCP
	switch m.AuthMode {
	case "token":
		if m.Token == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=token requires MCP_TOKEN (mint with `analytics keygen -mcp`)")
		}
	case "oauth":
		if m.Issuer == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=oauth requires MCP_AUTH_ISSUER")
		}
		if m.ResourceURL == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=oauth requires MCP_RESOURCE_URL")
		}
	case "cloudflare":
		if m.CFTeamDomain == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=cloudflare requires MCP_CF_TEAM_DOMAIN")
		}
		if m.CFAud == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=cloudflare requires MCP_CF_AUD")
		}
	case "":
		return fmt.Errorf("config: -mcp requires MCP_AUTH_MODE (oauth, cloudflare or token)")
	default:
		return fmt.Errorf("config: unknown MCP_AUTH_MODE %q (oauth, cloudflare or token)", m.AuthMode)
	}
	return nil
}
```

- [ ] **Step 5: Run and commit**

Run: `go test ./internal/config/ -v` → PASS.

```bash
git add go.mod go.sum internal/config/
git commit -m "feat(config): MCP_* settings with fail-fast validation; pin go-sdk v1.7.0"
```

### Task 14: Read-only database access

**Files:**
- Create: `internal/mcpserver/readdb.go`
- Create: `internal/mcpserver/readdb_test.go`

**Interfaces:**
- Produces (used by every read tool and the query tool):

```go
func OpenReadDB(path string) (*sql.DB, error)   // mode=ro + query_only, pooled
// queryRows runs q with args under the given timeout and returns
// column names plus rows of stringified values, capped at max+1 so the
// caller can detect truncation.
func queryRows(ctx context.Context, db *sql.DB, timeout time.Duration, max int, q string, args ...any) (cols []string, rows [][]string, truncated bool, err error)
```

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/readdb_test.go`:

```go
package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

// seedDB migrates a fresh database and returns its path. Every
// mcpserver test builds on this.
func seedDB(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/read.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(context.Background(), store.RegistryProject{
		Alias: "blog", Name: "My blog", Identity: "identified", AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create", Subject: "blog"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

func TestOpenReadDBIsReadOnly(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('x','y')`); err == nil {
		t.Fatal("write through the read connection succeeded")
	}
}

func TestQueryRowsCapsAndTruncates(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cols, rows, truncated, err := queryRows(context.Background(), db,
		time.Second, 2, `WITH n(i) AS (VALUES (1),(2),(3),(4)) SELECT i FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0] != "i" {
		t.Fatalf("cols = %v", cols)
	}
	if len(rows) != 2 || !truncated {
		t.Fatalf("rows = %d truncated = %v", len(rows), truncated)
	}
}

func TestQueryRowsTimeout(t *testing.T) {
	db, err := OpenReadDB(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a recursive CTE that never finishes without the deadline
	_, _, _, err = queryRows(context.Background(), db, 50*time.Millisecond, 10,
		`WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r) SELECT COUNT(*) FROM r`)
	if err == nil {
		t.Fatal("runaway query did not time out")
	}
}
```

- [ ] **Step 2: Run to verify failure** — package doesn't exist yet.

- [ ] **Step 3: Implement `internal/mcpserver/readdb.go`**

```go
// Package mcpserver implements the MCP endpoint (endpoint spec): an
// authenticated, read-only query surface plus the management tools.
// It never logs request bodies or bearer tokens; submitted SQL is
// logged at debug only.
package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// OpenReadDB opens the database file read-only with its own pool,
// independent of the single-writer store connection (endpoint spec §6.1).
// query_only is belt over the mode=ro braces: even a bug that finds a
// writable path is refused by the connection itself.
func OpenReadDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?mode=ro" +
		"&_pragma=query_only(1)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	return db, nil
}

func queryRows(ctx context.Context, db *sql.DB, timeout time.Duration, max int, q string, args ...any) ([]string, [][]string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	var out [][]string
	truncated := false
	for rows.Next() {
		if len(out) == max {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		for i := range vals {
			vals[i] = new(sql.NullString)
		}
		if err := rows.Scan(vals...); err != nil {
			return nil, nil, false, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			ns := v.(*sql.NullString)
			if ns.Valid {
				row[i] = ns.String
			}
		}
		out = append(out, row)
	}
	return cols, out, truncated, rows.Err()
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): read-only pool and bounded row scanning"
```

### Task 15: The three token verifiers

**Files:**
- Create: `internal/mcpserver/auth.go`
- Create: `internal/mcpserver/auth_test.go`

**Interfaces:**
- Consumes: `github.com/modelcontextprotocol/go-sdk/auth` (`TokenVerifier`, `TokenInfo`), `github.com/golang-jwt/jwt/v5`, `config.MCPConfig`.
- Produces:

```go
func StaticVerifier(token string) auth.TokenVerifier
func NewJWKSCache(jwksURL string, client *http.Client) *JWKSCache
func (c *JWKSCache) Key(kid string) (any, error)   // refetch on unknown kid, min 1m between fetches
func OAuthVerifier(issuer, audience string, cache *JWKSCache) auth.TokenVerifier
func CloudflareVerifier(teamDomain, aud string, cache *JWKSCache) auth.TokenVerifier
func DiscoverJWKSURL(ctx context.Context, issuer string, client *http.Client) (string, error)
var allowedAlgs = []string{"RS256","RS384","RS512","ES256","ES384","ES512","PS256","PS384","PS512"}
```

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/auth_test.go` — the JWKS fixture is the heart of it:

```go
package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type jwksFixture struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
	issuer string
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &jwksFixture{key: key, kid: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.issuer, "jwks_uri": f.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := f.key.Public().(*rsa.PublicKey)
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": f.kid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	f.server = httptest.NewServer(mux)
	f.issuer = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

func (f *jwksFixture) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *jwksFixture) claims(over jwt.MapClaims) jwt.MapClaims {
	c := jwt.MapClaims{
		"iss": f.issuer, "aud": "https://analytics.example.com/mcp",
		"sub": "user@example.com", "exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

func TestOAuthVerifier(t *testing.T) {
	f := newJWKSFixture(t)
	url, err := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	v := OAuthVerifier(f.issuer, "https://analytics.example.com/mcp",
		NewJWKSCache(url, f.server.Client()))

	info, err := v(context.Background(), f.sign(t, f.claims(nil)), nil)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if info.UserID != "user@example.com" {
		t.Errorf("UserID = %q", info.UserID)
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"expired", f.sign(t, f.claims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}))},
		{"future nbf", f.sign(t, f.claims(jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()}))},
		{"wrong aud", f.sign(t, f.claims(jwt.MapClaims{"aud": "https://other.example.com"}))},
		{"wrong iss", f.sign(t, f.claims(jwt.MapClaims{"iss": "https://evil.example.com"}))},
		{"garbage", "not.a.jwt"},
	}
	for _, tc := range bad {
		if _, err := v(context.Background(), tc.tok, nil); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestOAuthVerifierRejectsHMACAndNone(t *testing.T) {
	f := newJWKSFixture(t)
	url, _ := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	v := OAuthVerifier(f.issuer, "https://analytics.example.com/mcp",
		NewJWKSCache(url, f.server.Client()))
	// HMAC token signed with an arbitrary secret; alg allowlist must
	// reject it before any key lookup happens.
	hm := jwt.NewWithClaims(jwt.SigningMethodHS256, f.claims(nil))
	hm.Header["kid"] = f.kid
	s, err := hm.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), s, nil); err == nil {
		t.Fatal("HS256 accepted")
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, f.claims(nil))
	sn, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), sn, nil); err == nil {
		t.Fatal("alg=none accepted")
	}
}

func TestJWKSCacheRefetchesOnUnknownKid(t *testing.T) {
	f := newJWKSFixture(t)
	url, _ := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	cache := NewJWKSCache(url, f.server.Client())
	if _, err := cache.Key("k1"); err != nil {
		t.Fatal(err)
	}
	// rotate: new key id served by the fixture
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.key, f.kid = newKey, "k2"
	cache.minRefetch = 0 // the test may not wait a minute
	if _, err := cache.Key("k2"); err != nil {
		t.Fatalf("rotation not picked up: %v", err)
	}
}

func TestStaticVerifier(t *testing.T) {
	v := StaticVerifier("ar_secret")
	if info, err := v(context.Background(), "ar_secret", nil); err != nil || info.UserID != "mcp" {
		t.Fatalf("valid: %v %+v", err, info)
	}
	for _, bad := range []string{"", "ar_", "ar_secre", "ar_secretX", "wrong"} {
		if _, err := v(context.Background(), bad, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCloudflareVerifier(t *testing.T) {
	f := newJWKSFixture(t)
	cache := NewJWKSCache(f.issuer+"/jwks", f.server.Client())
	v := CloudflareVerifier(f.issuer, "aud-tag-1", cache)

	tok := f.sign(t, jwt.MapClaims{
		"iss": f.issuer, "aud": "aud-tag-1", "sub": "user@example.com",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", tok)
	// the opaque bearer is ignored entirely
	info, err := v(context.Background(), "oauth:opaque-ignored", req)
	if err != nil {
		t.Fatalf("valid assertion rejected: %v", err)
	}
	if info.UserID != "user@example.com" {
		t.Errorf("UserID = %q", info.UserID)
	}
	// missing header
	if _, err := v(context.Background(), "x", httptest.NewRequest("POST", "/mcp", nil)); err == nil {
		t.Fatal("missing assertion accepted")
	}
	// wrong aud tag
	wrong := f.sign(t, jwt.MapClaims{"iss": f.issuer, "aud": "other-tag",
		"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	req2 := httptest.NewRequest("POST", "/mcp", nil)
	req2.Header.Set("Cf-Access-Jwt-Assertion", wrong)
	if _, err := v(context.Background(), "x", req2); err == nil {
		t.Fatal("wrong aud accepted")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver/ -run 'TestOAuth|TestJWKS|TestStatic|TestCloudflare' -v` → FAIL.

- [ ] **Step 3: Implement `internal/mcpserver/auth.go`**

```go
package mcpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// allowedAlgs is the asymmetric allowlist (endpoint spec §5.2): never
// `none`, never HMAC — a JWKS public key must not be usable as an HMAC
// secret.
var allowedAlgs = []string{
	"RS256", "RS384", "RS512",
	"ES256", "ES384", "ES512",
	"PS256", "PS384", "PS512",
}

// StaticVerifier implements MCP_AUTH_MODE=token: constant-time compare
// against the single MCP_TOKEN. The middleware wrapping it must allow
// missing expiration (a static token has no exp).
func StaticVerifier(token string) auth.TokenVerifier {
	want := []byte(token)
	return func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare(want, []byte(got)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: "mcp"}, nil
	}
}

// DiscoverJWKSURL resolves the issuer's RFC 8414 metadata to its
// jwks_uri. Called once at startup so a typo'd issuer fails the boot,
// not the first request.
func DiscoverJWKSURL(ctx context.Context, issuer string, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		issuer+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mcpserver: issuer metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcpserver: issuer metadata: HTTP %d", resp.StatusCode)
	}
	var meta struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("mcpserver: issuer metadata: %w", err)
	}
	if meta.JWKSURI == "" {
		return "", fmt.Errorf("mcpserver: issuer metadata has no jwks_uri")
	}
	return meta.JWKSURI, nil
}

// JWKSCache fetches and caches an issuer's keys, refetching when an
// unknown kid appears (key rotation) but at most once per minRefetch.
type JWKSCache struct {
	url    string
	client *http.Client

	mu         sync.Mutex
	keys       map[string]any // kid -> *rsa.PublicKey | *ecdsa.PublicKey
	lastFetch  time.Time
	minRefetch time.Duration
}

func NewJWKSCache(jwksURL string, client *http.Client) *JWKSCache {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKSCache{url: jwksURL, client: client, minRefetch: time.Minute}
}

func (c *JWKSCache) Key(kid string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	if time.Since(c.lastFetch) < c.minRefetch {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	if err := c.fetchLocked(); err != nil {
		return nil, err
	}
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

func (c *JWKSCache) fetchLocked() error {
	resp, err := c.client.Get(c.url)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty, Kid, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("jwks parse: %w", err)
	}
	keys := map[string]any{}
	for _, k := range doc.Keys {
		switch k.Kty {
		case "RSA":
			n, err := base64.RawURLEncoding.DecodeString(k.N)
			if err != nil {
				continue
			}
			e, err := base64.RawURLEncoding.DecodeString(k.E)
			if err != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(n),
				E: int(new(big.Int).SetBytes(e).Int64()),
			}
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			case "P-521":
				curve = elliptic.P521()
			default:
				continue
			}
			x, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil {
				continue
			}
			y, err := base64.RawURLEncoding.DecodeString(k.Y)
			if err != nil {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{Curve: curve,
				X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		}
	}
	c.keys = keys
	c.lastFetch = time.Now()
	return nil
}

func (c *JWKSCache) keyfunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	return c.Key(kid)
}

// verifyJWT is the shared body of the oauth and cloudflare verifiers.
func verifyJWT(raw, issuer, audience string, cache *JWKSCache) (*auth.TokenInfo, error) {
	tok, err := jwt.Parse(raw, cache.keyfunc,
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	sub, _ := claims.GetSubject()
	exp, _ := claims.GetExpirationTime()
	info := &auth.TokenInfo{UserID: sub}
	if exp != nil {
		info.Expiration = exp.Time
	}
	if scope, ok := claims["scope"].(string); ok && scope != "" {
		info.Scopes = splitScopes(scope)
	}
	return info, nil
}

func splitScopes(s string) []string {
	var out []string
	for _, f := range splitOnSpace(s) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func splitOnSpace(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return out
}

// OAuthVerifier implements MCP_AUTH_MODE=oauth: the bearer token itself
// is a JWT from the external IdP.
func OAuthVerifier(issuer, audience string, cache *JWKSCache) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		return verifyJWT(token, issuer, audience, cache)
	}
}

// CloudflareVerifier implements MCP_AUTH_MODE=cloudflare (endpoint spec
// §5.2): under Access managed OAuth the bearer is opaque and validated
// at the edge; the origin validates the resolved identity JWT in
// Cf-Access-Jwt-Assertion. This is also what closes the
// direct-to-origin bypass — no valid assertion, no access.
func CloudflareVerifier(teamDomain, aud string, cache *JWKSCache) auth.TokenVerifier {
	issuer := "https://" + teamDomain
	return func(_ context.Context, _ string, req *http.Request) (*auth.TokenInfo, error) {
		if req == nil {
			return nil, auth.ErrInvalidToken
		}
		assertion := req.Header.Get("Cf-Access-Jwt-Assertion")
		if assertion == "" {
			return nil, fmt.Errorf("%w: no Cf-Access-Jwt-Assertion header", auth.ErrInvalidToken)
		}
		return verifyJWT(assertion, issuer, aud, cache)
	}
}
```

Note for the test fixture: `CloudflareVerifier` builds `https://` + teamDomain, but the httptest issuer is already a full `http://127.0.0.1:…` URL. Make the constructor tolerant:

```go
issuer := teamDomain
if !strings.HasPrefix(issuer, "http://") && !strings.HasPrefix(issuer, "https://") {
	issuer = "https://" + issuer
}
```

(add `"strings"` to imports). Production config passes the bare team domain; tests pass the full URL.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/auth.go internal/mcpserver/auth_test.go
git commit -m "feat(mcpserver): token, oauth and cloudflare verifiers with JWKS cache"
```

### Task 16: Read tools — projects, web, app

**Files:**
- Create: `internal/mcpserver/tools_read.go`
- Create: `internal/mcpserver/tools_read_test.go`
- Create: `internal/mcpserver/seed_test.go` (shared fixture: seeded DB + registry + tool host)

**Interfaces:**
- Consumes: Task 14 `queryRows`, `manage.Registry`, SDK `mcp.AddTool`.
- Produces: the `host` struct every tool hangs off, and the first five tools. Later tool tasks reuse `host` and `newTestHost`.

```go
type host struct {
    db      *sql.DB
    reg     *manage.Registry
    ops     *manage.Ops          // nil until Task 19 wires it
    timeout time.Duration
    maxRows int
    logger  *slog.Logger
}
type rangeIn struct {
    Project string `json:"project" jsonschema:"project alias, see list_projects"`
    From    string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
    To      string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}
func (h *host) register(s *mcp.Server)   // grows with each tool task
```

- [ ] **Step 1: Write the shared fixture**

`internal/mcpserver/seed_test.go`:

```go
package mcpserver

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

var testRetention = config.Retention{
	Web:     config.RetentionClass{RawDays: 7, AggregateDays: 365},
	Product: config.RetentionClass{RawDays: 30, AggregateDays: 365},
	App:     config.RetentionClass{RawDays: 30, AggregateDays: 365},
}

// newTestHost seeds two projects (blog: identified, docs: anonymous),
// two days of web aggregates and one raw hit, and returns a connected
// in-memory MCP client session against the assembled tool host.
func newTestHost(t *testing.T) (*host, *mcp.ClientSession) {
	t.Helper()
	path := t.TempDir() + "/mcp.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for alias, identity := range map[string]string{"blog": "identified", "docs": "anonymous"} {
		if err := st.CreateProject(ctx, store.RegistryProject{
			Alias: alias, Name: alias, Identity: identity, AllowedOrigins: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create", Subject: alias}); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := rawExec(st, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO agg_web_daily (project, day, visitors, pageviews, sessions, bounces, duration_sec)
	      VALUES ('blog','2026-08-20',10,25,12,3,600), ('blog','2026-08-21',12,30,14,4,720)`)
	seed(`INSERT INTO agg_web_pages (project, day, path, visitors, pageviews)
	      VALUES ('blog','2026-08-20','/post-1',8,15), ('blog','2026-08-20','/post-2',4,10)`)
	seed(`INSERT INTO web_hits (id, project, ts, received_at, actor_id, path, referrer_source,
	      utm_source, utm_medium, utm_campaign, country, device, browser, os, user_id, group_id)
	      VALUES ('h1','blog','2026-08-26T10:00:00Z','2026-08-26T10:00:00Z','a1','/live','','','','','','','','','u1','')`)

	db, err := OpenReadDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, testRetention, logger)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h := &host{db: db, reg: reg, ops: manage.NewOps(reg, st),
		timeout: 5 * time.Second, maxRows: 1000, logger: logger}

	srv := mcp.NewServer(&mcp.Implementation{Name: "analytics", Version: "test"}, nil)
	h.register(srv)
	ct, stEnd := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, stEnd, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return h, cs
}

// rawExec reaches the underlying *sql.DB of the sqlite store for seeding.
// The store interface deliberately has no Exec; add a test-only accessor
// in the sqlite package:
//
//   // ExecForTest is exported for test seeding only.
//   func (d *DB) ExecForTest(q string, args ...any) (sql.Result, error) { return d.db.Exec(q, args...) }
//
func rawExec(st store.Store, q string, args ...any) (sql.Result, error) {
	return st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...)
}

// callTool invokes a tool over the session and fails the test on
// protocol errors; tool errors come back in the result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}
```

(Add `ExecForTest` to `internal/store/sqlite/sqlite.go` as shown in the comment.)

- [ ] **Step 2: Write the failing tool tests**

`internal/mcpserver/tools_read_test.go`:

```go
package mcpserver

import (
	"strings"
	"testing"
)

func TestListProjects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "list_projects", nil)
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"blog", "identified", "docs", "anonymous"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestWebOverviewStitchesAggregatedAndLive(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_overview", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	// two aggregated days plus the live day from the raw hit
	for _, day := range []string{"2026-08-20", "2026-08-21", "2026-08-26"} {
		if !strings.Contains(out, day) {
			t.Errorf("missing day %s in %s", day, out)
		}
	}
	if !strings.Contains(out, "bounce_rate") {
		t.Errorf("no derived bounce_rate in %s", out)
	}
}

func TestWebBreakdownDimensions(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "pages", "limit": 1})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "/post-1") {
		t.Errorf("top page missing: %s", out)
	}
	if strings.Contains(out, "/post-2") {
		t.Errorf("limit 1 not applied: %s", out)
	}
	// invalid dimension is a tool error listing the valid ones
	res = callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "pages") {
		t.Errorf("bad dimension: %v %s", res.IsError, textOf(res))
	}
}

func TestUnknownProjectListsAliases(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_overview", map[string]any{
		"project": "nope", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown project did not error")
	}
	if out := textOf(res); !strings.Contains(out, "blog") || !strings.Contains(out, "docs") {
		t.Errorf("error must list valid aliases: %s", out)
	}
}
```

- [ ] **Step 3: Run to verify failure** — `go test ./internal/mcpserver/ -run 'TestList|TestWeb|TestUnknown' -v` → FAIL (`host` undefined).

- [ ] **Step 4: Implement `internal/mcpserver/tools_read.go`**

```go
package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type host struct {
	db      *sql.DB
	reg     *manage.Registry
	ops     *manage.Ops
	timeout time.Duration
	maxRows int
	logger  *slog.Logger
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type rangeIn struct {
	Project string `json:"project" jsonschema:"project alias; call list_projects first"`
	From    string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To      string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}

// checkRange validates the shared inputs. Error text is written for a
// model to recover from (endpoint spec §10): an unknown project lists
// the valid aliases instead of just refusing.
func (h *host) checkRange(ctx context.Context, in rangeIn) error {
	if !dayRe.MatchString(in.From) || !dayRe.MatchString(in.To) {
		return fmt.Errorf("from and to must be YYYY-MM-DD, got %q and %q", in.From, in.To)
	}
	s := h.reg.Snapshot(ctx)
	if s.Project(in.Project) == nil {
		var aliases []string
		for _, p := range s.Projects() {
			aliases = append(aliases, p.Alias)
		}
		sort.Strings(aliases)
		return fmt.Errorf("unknown project %q; valid aliases: %s",
			in.Project, strings.Join(aliases, ", "))
	}
	return nil
}

type tableOut struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func (h *host) table(ctx context.Context, q string, args ...any) (tableOut, error) {
	cols, rows, truncated, err := queryRows(ctx, h.db, h.timeout, h.maxRows, q, args...)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "context deadline") {
			return tableOut{}, fmt.Errorf("query exceeded %s; narrow the date range", h.timeout)
		}
		return tableOut{}, err
	}
	out := tableOut{Columns: cols, Rows: rows, Truncated: truncated}
	if truncated {
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", h.maxRows)
	}
	return out, nil
}

// ---- list_projects ----

type projectOut struct {
	Alias    string `json:"alias"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Archived bool   `json:"archived,omitempty"`
	FirstWebDay string `json:"first_web_day,omitempty"`
	LastWebDay  string `json:"last_web_day,omitempty"`
	FirstAppDay string `json:"first_app_day,omitempty"`
	LastAppDay  string `json:"last_app_day,omitempty"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (h *host) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listProjectsOut, error) {
	var out listProjectsOut
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		po := projectOut{Alias: p.Alias, Name: p.Name, Identity: p.Identity, Archived: p.Archived}
		// coverage probe: cheap MIN/MAX over the stitch views
		for _, probe := range []struct {
			view  string
			first *string
			last  *string
		}{
			{"v_web_daily", &po.FirstWebDay, &po.LastWebDay},
			{"v_app_daily", &po.FirstAppDay, &po.LastAppDay},
		} {
			_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
				`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM `+probe.view+` WHERE project=?`, p.Alias)
			if err != nil {
				return nil, out, err
			}
			if len(rows) == 1 {
				*probe.first, *probe.last = rows[0][0], rows[0][1]
			}
		}
		out.Projects = append(out.Projects, po)
	}
	return nil, out, nil
}

// ---- web_overview ----

func (h *host) webOverview(ctx context.Context, _ *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return nil, tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, visitors, pageviews, sessions, bounces, duration_sec,
		ROUND(CAST(bounces AS REAL)/MAX(sessions,1), 3) AS bounce_rate,
		CAST(duration_sec/MAX(sessions,1) AS INTEGER) AS avg_session_sec
		FROM v_web_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return nil, out, err
}

// ---- web_breakdown ----

// webDimensions maps the dimension enum to view + value column. The
// enum in the input schema is generated from this map, so tool text and
// behaviour cannot drift.
var webDimensions = map[string]struct{ view, col string }{
	"pages":     {"v_web_pages", "path"},
	"referrers": {"v_web_referrers", "source"},
	"countries": {"v_web_countries", "country"},
	"devices":   {"v_web_devices", "device"},
	"browsers":  {"v_web_browsers", "browser"},
	"os":        {"v_web_os", "os"},
	"utm":       {"v_web_utm", "utm_source"},
}

// breakdownIn embeds rangeIn; encoding/json flattens embedded structs,
// and the SDK's jsonschema inference follows suit. Verify with one
// tools/list call during implementation that project/from/to appear as
// top-level properties — if they do not, inline the three fields here
// and in the other embedding inputs instead of embedding.
type breakdownIn struct {
	rangeIn
	Dimension string `json:"dimension" jsonschema:"one of: pages, referrers, countries, devices, browsers, os, utm"`
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20"`
}

func dimensionKeys(m map[string]struct{ view, col string }) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func (h *host) webBreakdown(ctx context.Context, _ *mcp.CallToolRequest, in breakdownIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	dim, ok := webDimensions[in.Dimension]
	if !ok {
		return nil, tableOut{}, fmt.Errorf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(webDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if in.Dimension == "utm" {
		out, err := h.table(ctx, `SELECT utm_source, utm_medium, utm_campaign,
			SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
			FROM v_web_utm WHERE project=? AND day BETWEEN ? AND ?
			GROUP BY utm_source, utm_medium, utm_campaign
			ORDER BY visitors DESC LIMIT ?`, in.Project, in.From, in.To, limit)
		return nil, out, err
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY visitors DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return nil, out, err
}

// ---- app_overview / app_breakdown ----

var appDimensions = map[string]struct{ view, col string }{
	"screens":   {"v_app_screens", "screen"},
	"versions":  {"v_app_versions", "app_version"},
	"os":        {"v_app_os", "os_version"},
	"devices":   {"v_app_devices", "device_model"},
	"countries": {"v_app_countries", "country"},
}

func (h *host) appOverview(ctx context.Context, _ *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return nil, tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, actives, views, sessions, duration_sec
		FROM v_app_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return nil, out, err
}

func (h *host) appBreakdown(ctx context.Context, _ *mcp.CallToolRequest, in breakdownIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	dim, ok := appDimensions[in.Dimension]
	if !ok {
		return nil, tableOut{}, fmt.Errorf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(appDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(actives) AS actives, SUM(views) AS views
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY actives DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return nil, out, err
}

// register adds every tool to the server. Read tools carry
// ReadOnlyHint; management tools (tools_manage.go) do not.
func (h *host) register(s *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "list_projects", Annotations: ro,
		Description: "List projects with identity mode and data coverage. Call this first: every other tool takes a project alias from here. Projects with identity=identified support retention and identities; anonymous ones cannot (their visitor ids rotate daily)."},
		h.listProjects)
	mcp.AddTool(s, &mcp.Tool{Name: "web_overview", Annotations: ro,
		Description: "Daily web traffic for one project: visitors, pageviews, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Data includes yesterday and today (live)."},
		h.webOverview)
	mcp.AddTool(s, &mcp.Tool{Name: "web_breakdown", Annotations: ro,
		Description: "Top pages, referrers, countries, devices, browsers, os or utm for one project over a date range."},
		h.webBreakdown)
	mcp.AddTool(s, &mcp.Tool{Name: "app_overview", Annotations: ro,
		Description: "Daily app usage for one project: active users, screen views, sessions, duration."},
		h.appOverview)
	mcp.AddTool(s, &mcp.Tool{Name: "app_breakdown", Annotations: ro,
		Description: "Top screens, versions, os, devices or countries for one project's app traffic."},
		h.appBreakdown)
	h.registerProduct(s)
	h.registerQuery(s)
	h.registerManage(s)
}
```

`registerProduct`, `registerQuery`, `registerManage` do not exist yet — declare empty stubs at the bottom of this file and MOVE each to its own file in Tasks 17–19 where it gains its body:

```go
func (h *host) registerProduct(s *mcp.Server) {} // Task 17 moves this to tools_product.go
func (h *host) registerQuery(s *mcp.Server)   {} // Task 18 moves this to query.go
func (h *host) registerManage(s *mcp.Server)  {} // Task 19 moves this to tools_manage.go
```

- [ ] **Step 5: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/ internal/store/sqlite/sqlite.go
git commit -m "feat(mcpserver): list_projects and web/app read tools over the stitch views"
```

### Task 17: Read tools — product, retention, identities

**Files:**
- Create: `internal/mcpserver/tools_product.go` (moves `registerProduct` here)
- Create: `internal/mcpserver/tools_product_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/tools_product_test.go` — extend the fixture's seeding first: add to `newTestHost` (in `seed_test.go`):

```go
	seed(`INSERT INTO agg_product_daily (project, day, event_name, count, unique_users)
	      VALUES ('blog','2026-08-20','signup',5,4)`)
	seed(`INSERT INTO agg_product_totals (project, day, total_events, active_users)
	      VALUES ('blog','2026-08-20',5,4)`)
	seed(`INSERT INTO agg_retention (project, surface, cohort_day, day_offset, actors)
	      VALUES ('blog','web','2026-08-01',0,10), ('blog','web','2026-08-01',7,4)`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, hits, views, events)
	      VALUES ('blog','2026-08-20','user','u1',1,1,5,0,2)`)
	seed(`INSERT INTO identities (project, kind, id, name) VALUES ('blog','user','u1','Jane Doe')`)
```

Then the tests:

```go
package mcpserver

import (
	"strings"
	"testing"
)

func TestProductEvents(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_events", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "signup") {
		t.Errorf("missing event: %s", out)
	}
}

func TestProductAttributesExplainsWhenOff(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("aggregation-off project did not error")
	}
	if out := textOf(res); !strings.Contains(out, "product_aggregation") {
		t.Errorf("error must name the setting: %s", out)
	}
}

func TestRetentionReturnsCurveAndAggregatedThrough(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project": "blog", "surface": "web", "from": "2026-07-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"2026-08-01", "cohort_size", "aggregated_through"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestRetentionOnAnonymousProjectExplains(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project": "docs", "surface": "web", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("anonymous project retention did not error")
	}
	if out := textOf(res); !strings.Contains(out, "identified") {
		t.Errorf("error must explain the identity requirement: %s", out)
	}
}

func TestIdentities(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project": "blog", "kind": "user", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "u1") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("identities missing id or name: %s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure** — tools not registered → `CallTool` returns a tool-not-found error.

- [ ] **Step 3: Implement `internal/mcpserver/tools_product.go`** (delete the stub in `tools_read.go`)

```go
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type productEventsIn struct {
	rangeIn
	Event string `json:"event,omitempty" jsonschema:"filter to one event name"`
}

func (h *host) productEvents(ctx context.Context, _ *mcp.CallToolRequest, in productEventsIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	if in.Event != "" {
		out, err := h.table(ctx, `SELECT day, event_name, count, unique_users
			FROM v_product_daily WHERE project=? AND day BETWEEN ? AND ? AND event_name=?
			ORDER BY day`, in.Project, in.From, in.To, in.Event)
		return nil, out, err
	}
	out, err := h.table(ctx, `SELECT day, event_name, count, unique_users
		FROM v_product_daily WHERE project=? AND day BETWEEN ? AND ?
		ORDER BY day, count DESC`, in.Project, in.From, in.To)
	return nil, out, err
}

func (h *host) productAttributes(ctx context.Context, _ *mcp.CallToolRequest, in productEventsIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	p := h.reg.Snapshot(ctx).Project(in.Project)
	if p.Aggregation == nil || !p.Aggregation.Enabled {
		return nil, tableOut{}, fmt.Errorf(
			"project %q has product_aggregation disabled; enable it with `update_project` or `analytics project update` (attribute breakdowns are opt-in per key)", in.Project)
	}
	q := `SELECT day, event_name, attr_key, attr_value, count, unique_users
		FROM agg_product_attrs WHERE project=? AND day BETWEEN ? AND ?`
	args := []any{in.Project, in.From, in.To}
	if in.Event != "" {
		q += ` AND event_name=?`
		args = append(args, in.Event)
	}
	out, err := h.table(ctx, q+` ORDER BY day, count DESC`, args...)
	return nil, out, err
}

type retentionIn struct {
	rangeIn
	Surface string `json:"surface" jsonschema:"web or app; the two populations are cohorted separately"`
}

type retentionOut struct {
	tableOut
	AggregatedThrough string `json:"aggregated_through"`
}

func (h *host) retention(ctx context.Context, _ *mcp.CallToolRequest, in retentionIn) (*mcp.CallToolResult, retentionOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, retentionOut{}, err
	}
	if in.Surface != "web" && in.Surface != "app" {
		return nil, retentionOut{}, fmt.Errorf("surface must be web or app, got %q", in.Surface)
	}
	p := h.reg.Snapshot(ctx).Project(in.Project)
	if p.Identity != "identified" {
		return nil, retentionOut{}, fmt.Errorf(
			"project %q is anonymous: retention is undefined because visitor ids rotate daily; it requires the project setting identity=identified (a privacy-significant change — see the README's GDPR section)", in.Project)
	}
	tbl, err := h.table(ctx, `SELECT cohort_day, day_offset, actors, cohort_size
		FROM v_retention WHERE project=? AND surface=? AND cohort_day BETWEEN ? AND ?
		ORDER BY cohort_day, day_offset`, in.Project, in.Surface, in.From, in.To)
	if err != nil {
		return nil, retentionOut{}, err
	}
	out := retentionOut{tableOut: tbl}
	// v_retention has no live half: report how fresh it is so recent
	// cohorts are read as "not yet aggregated", never as zero.
	_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
		`SELECT COALESCE(MAX(cohort_day),'') FROM agg_retention WHERE project=?`, in.Project)
	if err == nil && len(rows) == 1 {
		out.AggregatedThrough = rows[0][0]
	}
	if out.Note == "" {
		out.Note = "retention refreshes at the 03:00 UTC daily pass; cohorts after aggregated_through are absent, not zero"
	}
	return nil, out, nil
}

type identitiesIn struct {
	rangeIn
	Kind string `json:"kind" jsonschema:"user or group"`
	Limit int   `json:"limit,omitempty" jsonschema:"top-N by activity, default 50"`
}

func (h *host) identities(ctx context.Context, _ *mcp.CallToolRequest, in identitiesIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	if in.Kind != "user" && in.Kind != "group" {
		return nil, tableOut{}, fmt.Errorf("kind must be user or group, got %q", in.Kind)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	out, err := h.table(ctx, `SELECT d.id, COALESCE(i.name,'') AS name,
		SUM(d.actors) AS actors, SUM(d.hits) AS hits, SUM(d.views) AS views, SUM(d.events) AS events
		FROM v_identity_daily d
		LEFT JOIN identities i ON i.project=d.project AND i.kind=d.kind AND i.id=d.id
		WHERE d.project=? AND d.kind=? AND d.day BETWEEN ? AND ?
		GROUP BY d.id, i.name
		ORDER BY hits+views+events DESC LIMIT ?`,
		in.Project, in.Kind, in.From, in.To, limit)
	return nil, out, err
}

func (h *host) registerProduct(s *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "product_events", Annotations: ro,
		Description: "Opt-in product events per day: count and unique users per event name, plus daily totals."},
		h.productEvents)
	mcp.AddTool(s, &mcp.Tool{Name: "product_attributes", Annotations: ro,
		Description: "Attribute breakdowns for product events. Only meaningful where the project has product_aggregation enabled; the error explains the setting when it is not."},
		h.productAttributes)
	mcp.AddTool(s, &mcp.Tool{Name: "retention", Annotations: ro,
		Description: "D1/D7/D30-style cohort curves for identified projects. Returns aggregated_through: cohorts after it are absent (refreshed 03:00 UTC), not zero. Anonymous projects have no retention by design."},
		h.retention)
	mcp.AddTool(s, &mcp.Tool{Name: "identities", Annotations: ro,
		Description: "Per-user or per-group activity with display names. This surfaces personal data on identified projects."},
		h.identities)
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): product, retention and identities tools"
```

### Task 18: The `query` tool and schema resources

**Files:**
- Create: `internal/mcpserver/query.go` (moves `registerQuery` here)
- Create: `internal/mcpserver/query_test.go`
- Create: `internal/mcpserver/resources.go`
- Create: `internal/mcpserver/resources_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/query_test.go`:

```go
package mcpserver

import (
	"strings"
	"testing"
)

func TestQueryToolSelects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "query", map[string]any{
		"sql": "SELECT project, day, visitors FROM v_web_daily WHERE project='blog' ORDER BY day"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "2026-08-20") {
		t.Errorf("missing data: %s", out)
	}
}

func TestQueryToolBlocksWrites(t *testing.T) {
	_, cs := newTestHost(t)
	for _, q := range []string{
		"DELETE FROM web_hits",
		"INSERT INTO meta (key, value) VALUES ('x','y')",
		"UPDATE projects SET name='pwned'",
		"PRAGMA journal_mode=DELETE",
		"SELECT 1; DELETE FROM web_hits",
		"ATTACH DATABASE '/etc/passwd' AS pwn",
		"attach database '/tmp/x' as pwn",
		"WITH x AS (SELECT 1) ATTACH DATABASE '/tmp/x' AS pwn",
	} {
		res := callTool(t, cs, "query", map[string]any{"sql": q})
		if !res.IsError {
			t.Errorf("accepted: %s", q)
		}
	}
}

func TestQueryToolCapsRows(t *testing.T) {
	h, cs := newTestHost(t)
	h.maxRows = 1
	res := callTool(t, cs, "query", map[string]any{
		"sql": "WITH n(i) AS (VALUES (1),(2),(3)) SELECT i FROM n"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "PARTIAL") {
		t.Errorf("truncation not flagged: %s", out)
	}
}
```

`internal/mcpserver/resources_test.go`:

```go
package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSchemaResources(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://views"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Contents[0].Text
	for _, want := range []string{
		"v_web_daily", "v_retention", "YYYY-MM-DD",
		"03:00 UTC", "identified", "includes yesterday",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("schema://views missing %q", want)
		}
	}
	pres, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://projects"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pres.Contents[0].Text, "blog") {
		t.Errorf("schema://projects missing project: %s", pres.Contents[0].Text)
	}
}
```

(Server assembly in `newTestHost` gains `h.registerResources(srv)` next to `h.register(srv)` — add the call now.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver/ -run 'TestQuery|TestSchema' -v` → FAIL.

- [ ] **Step 3: Implement `internal/mcpserver/query.go`** (delete the stub)

```go
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type queryIn struct {
	SQL string `json:"sql" jsonschema:"a single read-only SELECT or WITH query against the v_* views; read schema://views first"`
}

// runQuery applies the four guard layers of endpoint spec §8:
//  1. the pool is mode=ro + query_only (writes cannot succeed),
//  2. the query is wrapped as a subquery — DDL/DML/PRAGMA/multi-statement
//     become syntax errors, and the row cap rides in the same clause,
//  3. ATTACH is rejected by token scan (read-only does not prevent it),
//  4. queryRows enforces the deadline.
func (h *host) runQuery(ctx context.Context, _ *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, tableOut, error) {
	if strings.TrimSpace(in.SQL) == "" {
		return nil, tableOut{}, fmt.Errorf("sql must not be empty")
	}
	upper := strings.ToUpper(in.SQL)
	if strings.Contains(upper, "ATTACH") {
		return nil, tableOut{}, fmt.Errorf("ATTACH is not allowed")
	}
	h.logger.Debug("mcp query", "sql", in.SQL) // debug only, never info (spec §8)
	wrapped := fmt.Sprintf("SELECT * FROM (%s\n) LIMIT %d",
		strings.TrimRight(strings.TrimSpace(in.SQL), ";"), h.maxRows+1)
	cols, rows, _, err := queryRows(ctx, h.db, h.timeout, h.maxRows, wrapped)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "context deadline") {
			return nil, tableOut{}, fmt.Errorf("query exceeded %s; narrow the date range or query agg_* tables directly", h.timeout)
		}
		return nil, tableOut{}, fmt.Errorf("SQL error (the query runs wrapped as a subquery; only single SELECT/WITH statements parse): %v", err)
	}
	out := tableOut{Columns: cols, Rows: rows}
	if len(rows) == h.maxRows {
		out.Truncated = true
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — add a WHERE or aggregate", h.maxRows)
	}
	return nil, out, nil
}

func (h *host) registerQuery(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "query",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Escape hatch: run one read-only SELECT/WITH against the v_* views and agg_* tables. Read schema://views first for columns and caveats. Row-capped and time-limited; the connection is read-only at the driver level."},
		h.runQuery)
}
```

A subtlety the wrap depends on: `queryRows` caps at `max` and reports `truncated` when it stops early, while the SQL `LIMIT max+1` guarantees at most one surplus row exists to detect. Both caps compose; neither alone is sufficient (`queryRows` can't see the surplus without the +1, the LIMIT can't flag truncation).

The multi-statement case deserves a comment in the code: `database/sql` with this driver already refuses multi-statement strings, and the wrap makes them a syntax error besides — belt and braces, verified by `TestQueryToolBlocksWrites`.

- [ ] **Step 4: Implement `internal/mcpserver/resources.go`**

```go
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaViews is the reference a model needs to write correct SQL. The
// three caveats at the top are the ones it cannot infer (endpoint spec §9).
const schemaViews = `# Query schema

Facts you cannot infer from the DDL:

1. day columns are TEXT 'YYYY-MM-DD' (UTC). Compare and BETWEEN as strings.
2. Every v_* view includes yesterday and today: each stitches aggregated
   history (agg_* tables) with a live half computed from raw rows.
3. EXCEPTION: v_retention has no live half. It refreshes at the 03:00 UTC
   daily pass; cohort days after that are ABSENT, not zero. It is populated
   only for projects with identity=identified (anonymous visitor ids rotate
   daily, so cohorts are undefined). Check list_projects for identity.

Views (all carry a 'project' column — always filter on it):

  v_web_daily(project, day, visitors, pageviews, sessions, bounces, duration_sec)
  v_web_pages(project, day, path, visitors, pageviews)
  v_web_referrers(project, day, source, visitors, pageviews)
  v_web_countries(project, day, country, visitors, pageviews)
  v_web_devices(project, day, device, visitors, pageviews)
  v_web_browsers(project, day, browser, visitors, pageviews)
  v_web_os(project, day, os, visitors, pageviews)
  v_web_utm(project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews)
  v_product_daily(project, day, event_name, count, unique_users)
  v_product_totals(project, day, total_events, active_users)
  v_app_daily(project, day, actives, views, sessions, duration_sec)
  v_app_screens(project, day, screen, actives, views)
  v_app_versions(project, day, platform, app_version, actives, views)
  v_app_os(project, day, platform, os_version, actives, views)
  v_app_devices(project, day, device_model, actives, views)
  v_app_countries(project, day, country, actives, views)
  v_identity_daily(project, day, kind, id, actors, users, hits, views, events)  -- kind: 'user'|'group'
  v_retention(project, surface, cohort_day, day_offset, actors, cohort_size)    -- surface: 'web'|'app'
  agg_product_attrs(project, day, event_name, attr_key, attr_value, count, unique_users)
  identities(project, kind, id, name)  -- display names, joinable to v_identity_daily

Cost note: the views' live halves sessionize raw rows with window
functions; a WHERE on day may not prune that work. Narrow ranges and the
agg_* tables are cheaper. Queries are row-capped and time-limited.`

func (h *host) registerResources(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI: "schema://views", Name: "views",
		Description: "Queryable views, their columns, and the caveats needed to write correct SQL. Read before using the query tool.",
		MIMEType:    "text/plain",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://views", MIMEType: "text/plain", Text: schemaViews}}}, nil
	})
	s.AddResource(&mcp.Resource{
		URI: "schema://projects", Name: "projects",
		Description: "Current projects with identity modes and settings.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		type pj struct {
			Alias, Name, Identity string
			Archived              bool `json:",omitempty"`
		}
		var out []pj
		for _, p := range h.reg.Snapshot(ctx).Projects() {
			out = append(out, pj{p.Alias, p.Name, p.Identity, p.Archived})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("schema://projects: %w", err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://projects", MIMEType: "application/json", Text: string(b)}}}, nil
	})
}
```

- [ ] **Step 5: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): guarded query tool and schema resources"
```

### Task 19: Management tools

**Files:**
- Create: `internal/mcpserver/tools_manage.go` (moves `registerManage` here)
- Create: `internal/mcpserver/tools_manage_test.go`

**Interfaces:**
- Consumes: `manage.Ops` (Tasks 3, 5), `manage.Snippet`. Actor is always `"mcp"`.
- Produces: the eight tools of managed-config spec §5. **No `delete_project`, no import/export** — CLI-only.

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/tools_manage_test.go`:

```go
package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateProjectToolReturnsSnippet(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"alias": "shop", "name": "My shop",
		"allowed_origins": []string{"https://shop.example.com"}})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"script.js", "ak_", "data-identity"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	// create_project with issue_key=true mints a first key; verify the
	// key resolves in the registry
	list := callTool(t, cs, "list_ingest_keys", map[string]any{"project": "shop"})
	if !strings.Contains(textOf(list), "default") {
		t.Errorf("no default key listed: %s", textOf(list))
	}
}

func TestArchiveRestoreTools(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "archive_project", map[string]any{"alias": "docs"}); res.IsError {
		t.Fatalf("archive: %s", textOf(res))
	}
	res := callTool(t, cs, "list_projects", nil)
	if !strings.Contains(textOf(res), "archived") {
		t.Errorf("archive not visible: %s", textOf(res))
	}
	if res := callTool(t, cs, "restore_project", map[string]any{"alias": "docs"}); res.IsError {
		t.Fatalf("restore: %s", textOf(res))
	}
}

func TestKeyToolsLifecycle(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project": "blog", "label": "ios"})
	if res.IsError {
		t.Fatalf("issue: %s", textOf(res))
	}
	if res := callTool(t, cs, "disable_ingest_key", map[string]any{
		"project": "blog", "label": "ios"}); res.IsError {
		t.Fatalf("disable: %s", textOf(res))
	}
	if res := callTool(t, cs, "enable_ingest_key", map[string]any{
		"project": "blog", "label": "ios"}); res.IsError {
		t.Fatalf("enable: %s", textOf(res))
	}
}

func TestNoDeleteToolExists(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "delete") {
			t.Errorf("irreversible tool exposed over MCP: %s", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("tool %s has no annotations", tool.Name)
		}
	}
}

func TestManagementToolsAnnotatedNonReadOnly(t *testing.T) {
	_, cs := newTestHost(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{"create_project": true, "update_project": true,
		"archive_project": true, "restore_project": true, "issue_ingest_key": true,
		"disable_ingest_key": true, "enable_ingest_key": true}
	for _, tool := range tools.Tools {
		if writers[tool.Name] && tool.Annotations.ReadOnlyHint {
			t.Errorf("%s marked read-only", tool.Name)
		}
		if !writers[tool.Name] && tool.Name != "query" && !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s not marked read-only", tool.Name)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — tools missing.

- [ ] **Step 3: Implement `internal/mcpserver/tools_manage.go`** (delete the stub)

```go
package mcpserver

import (
	"context"
	"fmt"

	"github.com/dmitry/analytics/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Management tools (managed-config spec §5). One authorization tier: the
// same token that reads. The guardrails against prompt injection from
// attacker-writable analytics data (spec §6): every write is annotated so
// clients interpose the operator, nothing here is irreversible, and every
// operation lands in audit_log as actor 'mcp'.

type projectIn struct {
	Alias          string   `json:"alias" jsonschema:"project alias (immutable)"`
	Name           string   `json:"name,omitempty" jsonschema:"display name, defaults to alias"`
	Identity       string   `json:"identity,omitempty" jsonschema:"anonymous (default) or identified. identified stores user ids and names as given — a privacy-significant setting; see the GDPR docs"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"origins allowed to post events"`
	// SkipKey rather than IssueKey: JSON booleans have no "unset", and
	// the zero value must give the default behaviour (issue a key).
	SkipKey bool `json:"skip_key,omitempty" jsonschema:"create_project only: set true to NOT issue a first ingest key"`
}

type projectToolOut struct {
	Alias    string `json:"alias"`
	Identity string `json:"identity"`
	Key      string `json:"key,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
}

func (h *host) createProject(ctx context.Context, _ *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, projectToolOut, error) {
	p, err := h.ops.CreateProject(ctx, "mcp", manage.ProjectSpec{
		Alias: in.Alias, Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins})
	if err != nil {
		return nil, projectToolOut{}, err
	}
	out := projectToolOut{Alias: p.Alias, Identity: p.Identity}
	// by default the quickstart story is one round trip to paste-ready
	if !in.SkipKey {
		key, err := h.ops.IssueIngestKey(ctx, "mcp", p.Alias, "default")
		if err != nil {
			return nil, out, fmt.Errorf("project created but key issue failed: %w", err)
		}
		origin := ""
		if len(p.AllowedOrigins) > 0 {
			origin = p.AllowedOrigins[0]
		}
		out.Key = key
		out.Snippet = manage.Snippet(origin, key, p.Identity)
	}
	return nil, out, nil
}
```

Continue the file:

```go
func (h *host) updateProject(ctx context.Context, _ *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, projectToolOut, error) {
	p, err := h.ops.UpdateProject(ctx, "mcp", manage.ProjectSpec{
		Alias: in.Alias, Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins})
	if err != nil {
		return nil, projectToolOut{}, err
	}
	return nil, projectToolOut{Alias: p.Alias, Identity: p.Identity}, nil
}

type aliasIn struct {
	Alias string `json:"alias" jsonschema:"project alias"`
}
type okOut struct {
	Status string `json:"status"`
}

func (h *host) archiveProject(ctx context.Context, _ *mcp.CallToolRequest, in aliasIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.ArchiveProject(ctx, "mcp", in.Alias); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "archived; ingestion rejected, data kept, reversible with restore_project"}, nil
}

func (h *host) restoreProject(ctx context.Context, _ *mcp.CallToolRequest, in aliasIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.RestoreProject(ctx, "mcp", in.Alias); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "restored"}, nil
}

type keyIn struct {
	Project string `json:"project" jsonschema:"project alias"`
	Label   string `json:"label" jsonschema:"key label, e.g. web, ios; unique per project"`
}
type keyOut struct {
	Key     string `json:"key,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Status  string `json:"status"`
}

func (h *host) issueKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, keyOut, error) {
	key, err := h.ops.IssueIngestKey(ctx, "mcp", in.Project, in.Label)
	if err != nil {
		return nil, keyOut{}, err
	}
	p := h.reg.Snapshot(ctx).Project(in.Project)
	origin := ""
	if len(p.AllowedOrigins) > 0 {
		origin = p.AllowedOrigins[0]
	}
	return nil, keyOut{Key: key, Snippet: manage.Snippet(origin, key, p.Identity), Status: "issued"}, nil
}

func (h *host) disableKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.DisableIngestKey(ctx, "mcp", in.Project, in.Label); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "disabled; reversible with enable_ingest_key"}, nil
}

func (h *host) enableKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.EnableIngestKey(ctx, "mcp", in.Project, in.Label); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "enabled"}, nil
}

type listKeysIn struct {
	Project string `json:"project,omitempty" jsonschema:"filter to one project"`
}
type listKeysOut struct {
	Keys []keyRow `json:"keys"`
}
type keyRow struct {
	Project, Label, Key, State string
}

func (h *host) listKeys(ctx context.Context, _ *mcp.CallToolRequest, in listKeysIn) (*mcp.CallToolResult, listKeysOut, error) {
	_, ks, err := h.ops.St.LoadRegistry(ctx)
	if err != nil {
		return nil, listKeysOut{}, err
	}
	var out listKeysOut
	for _, k := range ks {
		if in.Project != "" && k.Project != in.Project {
			continue
		}
		state := "active"
		if k.Disabled {
			state = "disabled"
		}
		out.Keys = append(out.Keys, keyRow{k.Project, k.Label, k.Key, state})
	}
	return nil, out, nil
}

func (h *host) registerManage(s *mcp.Server) {
	no := false // DestructiveHint is *bool in the SDK; nothing here destroys
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "create_project", Annotations: write,
		Description: "Create a project and (by default) its first ingest key; returns a paste-ready embed snippet. Set skip_key to suppress the key."},
		h.createProject)
	mcp.AddTool(s, &mcp.Tool{Name: "update_project", Annotations: write,
		Description: "Replace a project's name, identity mode and allowed origins. Switching to identity=identified starts storing user ids and names as given — privacy-significant, say so to the user before doing it."},
		h.updateProject)
	mcp.AddTool(s, &mcp.Tool{Name: "archive_project", Annotations: idem,
		Description: "Archive a project: ingestion stops, data and dashboards keep working, fully reversible with restore_project. There is no delete over MCP — deletion requires the CLI."},
		h.archiveProject)
	mcp.AddTool(s, &mcp.Tool{Name: "restore_project", Annotations: idem,
		Description: "Restore an archived project."},
		h.restoreProject)
	mcp.AddTool(s, &mcp.Tool{Name: "issue_ingest_key", Annotations: write,
		Description: "Issue a new ingest key for a project. Ingest keys are public identifiers (they ship in page source); retirement is disable, not secrecy."},
		h.issueKey)
	mcp.AddTool(s, &mcp.Tool{Name: "disable_ingest_key", Annotations: idem,
		Description: "Disable an ingest key by project and label; events with it are rejected within a second. Reversible."},
		h.disableKey)
	mcp.AddTool(s, &mcp.Tool{Name: "enable_ingest_key", Annotations: idem,
		Description: "Re-enable a disabled ingest key."},
		h.enableKey)
	mcp.AddTool(s, &mcp.Tool{Name: "list_ingest_keys",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "List ingest keys with their state, including disabled ones."},
		h.listKeys)
}
```

Check the actual field shape of `mcp.ToolAnnotations.DestructiveHint` (`*bool`) and `ReadOnlyHint`/`IdempotentHint` (`bool`) against the SDK before committing — the mixed pointer/value shapes above follow v1.7.0's `go doc`; clean up the `t`/`f` helpers to only what's used.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS (including the two annotation-contract tests).

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): management tools with reversibility and annotation guardrails"
```

### Task 20: Endpoint assembly

**Files:**
- Create: `internal/mcpserver/server.go`
- Create: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: everything above, SDK `mcp.NewStreamableHTTPHandler`, `auth.RequireBearerToken`, `auth.ProtectedResourceMetadataHandler`, `oauthex.ProtectedResourceMetadata`.
- Produces (used by `app` in Task 21):

```go
// NewHandler assembles the MCP endpoint: tool host, streamable transport,
// auth middleware, and (mode-dependent) the RFC 9728 metadata route.
// Routes registered on the returned mux: /mcp, /healthz, and
// /.well-known/oauth-protected-resource when issuer is configured.
func NewHandler(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error)
// The func() error closes the read DB. registerOn is the shared-mux
// variant used when MCP_ADDR == LISTEN_ADDR (Task 21):
func RegisterOn(mux *http.ServeMux, h http.Handler, cfg *config.Config, withHealthz bool)
```

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/server_test.go`:

```go
package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func newHandlerFixture(t *testing.T, over map[string]string) http.Handler {
	t.Helper()
	path := seedDB(t) // from readdb_test.go: migrated DB with project 'blog'
	base := map[string]string{
		"DATABASE_URL":  "sqlite://" + path,
		"MCP_AUTH_MODE": "token",
		"MCP_TOKEN":     "ar_testtoken",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	cfg, err := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateMCP(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, cfg.Retention, logger)
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB() })
	return h
}

func TestMCPRequires401WithChallenge(t *testing.T) {
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_ISSUER":  "https://idp.example.com",
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
	www := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(www, "resource_metadata") {
		t.Errorf("WWW-Authenticate = %q; must point at the metadata URL", www)
	}
}

func TestMCPTokenAuthPasses(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "analytics") {
		t.Errorf("no serverInfo in %s", rec.Body.String())
	}
}

func TestMCPWrongTokenRejected(t *testing.T) {
	h := newHandlerFixture(t, nil)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestPRMServedWhenIssuerConfigured(t *testing.T) {
	h := newHandlerFixture(t, map[string]string{
		"MCP_AUTH_ISSUER":  "https://idp.example.com",
		"MCP_RESOURCE_URL": "https://analytics.example.com/mcp"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "idp.example.com") || !strings.Contains(body, "analytics.example.com") {
		t.Errorf("metadata = %s", body)
	}
}

func TestPRM404WithoutIssuer(t *testing.T) {
	h := newHandlerFixture(t, nil) // token mode, no issuer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestTokenNeverLoggedAtInfo(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	path := seedDB(t)
	base := map[string]string{"DATABASE_URL": "sqlite://" + path,
		"MCP_AUTH_MODE": "token", "MCP_TOKEN": "ar_secrettoken"}
	cfg, _ := config.FromEnv(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	st, _ := store.Open(cfg.Database)
	defer st.Close()
	reg := manage.New(st, cfg.Retention, logger)
	reg.Reload(context.Background())
	h, closeDB, err := NewHandler(context.Background(), cfg, reg, manage.NewOps(reg, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ar_secrettoken")
	h.ServeHTTP(httptest.NewRecorder(), req)
	req2 := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer ar_wrongtoken")
	h.ServeHTTP(httptest.NewRecorder(), req2)
	for _, secret := range []string{"ar_secrettoken", "ar_wrongtoken"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("token %q appeared in info-level logs", secret)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `NewHandler` undefined.

- [ ] **Step 3: Implement `internal/mcpserver/server.go`**

```go
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// NewHandler assembles the MCP endpoint (endpoint spec §3, §5).
func NewHandler(ctx context.Context, cfg *config.Config, reg *manage.Registry, ops *manage.Ops, logger *slog.Logger) (http.Handler, func() error, error) {
	db, err := OpenReadDB(cfg.MCP.DBPath)
	if err != nil {
		return nil, nil, err
	}
	h := &host{db: db, reg: reg, ops: ops,
		timeout: cfg.MCP.QueryTimeout, maxRows: cfg.MCP.QueryMaxRows, logger: logger}
	srv := mcp.NewServer(&mcp.Implementation{Name: "analytics", Version: "1.0.0"}, nil)
	h.register(srv)
	h.registerResources(srv)
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil)

	protected, err := wrapAuth(ctx, cfg.MCP, streamable)
	if err != nil {
		db.Close()
		return nil, nil, err
	}

	mux := http.NewServeMux()
	RegisterOn(mux, protected, cfg, true)
	return mux, db.Close, nil
}

// RegisterOn mounts the MCP routes on a mux. withHealthz=false when the
// mux is shared with the ingest surface, whose /healthz already exists
// (ServeMux panics on duplicate patterns).
func RegisterOn(mux *http.ServeMux, protected http.Handler, cfg *config.Config, withHealthz bool) {
	mux.Handle("/mcp", protected)
	if cfg.MCP.Issuer != "" && cfg.MCP.AuthMode != "cloudflare" {
		meta := &oauthex.ProtectedResourceMetadata{
			Resource:             cfg.MCP.ResourceURL,
			AuthorizationServers: []string{cfg.MCP.Issuer},
		}
		mux.Handle("GET /.well-known/oauth-protected-resource",
			auth.ProtectedResourceMetadataHandler(meta))
	}
	if withHealthz {
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
	}
}

// wrapAuth builds the mode's verifier and middleware (endpoint spec §5.2).
func wrapAuth(ctx context.Context, m config.MCPConfig, next http.Handler) (http.Handler, error) {
	switch m.AuthMode {
	case "token":
		opts := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
		if m.Issuer != "" {
			opts.ResourceMetadataURL = m.ResourceURL + "/.well-known/oauth-protected-resource"
		}
		return auth.RequireBearerToken(StaticVerifier(m.Token), opts)(next), nil
	case "oauth":
		jwksURL, err := DiscoverJWKSURL(ctx, m.Issuer, nil)
		if err != nil {
			return nil, fmt.Errorf("mcp oauth mode: %w (is MCP_AUTH_ISSUER correct and reachable?)", err)
		}
		v := OAuthVerifier(m.Issuer, m.Audience, NewJWKSCache(jwksURL, nil))
		return auth.RequireBearerToken(v, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURLFor(m.ResourceURL),
		})(next), nil
	case "cloudflare":
		// Access owns discovery and the 401 challenge at the edge; the
		// origin's only job is validating the assertion header. A thin
		// middleware instead of RequireBearerToken so a request whose
		// Authorization header Access did not populate is still judged
		// by the assertion alone (endpoint spec §5.2).
		cache := NewJWKSCache("https://"+m.CFTeamDomain+"/cdn-cgi/access/certs", nil)
		v := CloudflareVerifier(m.CFTeamDomain, m.CFAud, cache)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info, err := v(r.Context(), "", r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = info
			next.ServeHTTP(w, r)
		}), nil
	default:
		return nil, fmt.Errorf("mcpserver: unknown auth mode %q", m.AuthMode)
	}
}

func metadataURLFor(resourceURL string) string {
	// RFC 9728: the well-known path is host-rooted; the resource URL's
	// origin carries it. Good enough for the single-origin deployments
	// this server targets; revisit if a path-scoped resource needs the
	// path-suffix form.
	u := resourceURL
	for i := len("https://"); i < len(u); i++ {
		if u[i] == '/' {
			u = u[:i]
			break
		}
	}
	return u + "/.well-known/oauth-protected-resource"
}
```

Before finalizing: check `oauthex.ProtectedResourceMetadata` field names with `go doc github.com/modelcontextprotocol/go-sdk/oauthex.ProtectedResourceMetadata` — the plan assumes `Resource` and `AuthorizationServers`; use whatever the SDK actually names them. Same for whether `RequireBearerToken`'s 401 path emits `WWW-Authenticate` with `resource_metadata` from `ResourceMetadataURL` — `TestMCPRequires401WithChallenge` pins the behaviour.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/mcpserver/ -v` → PASS.

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(mcpserver): endpoint assembly with per-mode auth middleware"
```

### Task 21: `serve -api -mcp`

**Files:**
- Modify: `cmd/analytics/serve.go`, `cmd/analytics/main_test.go` or `commands_test.go`
- Modify: `internal/app/app.go`, `internal/app/app_test.go`
- Modify: `Dockerfile:52`, `deploy/systemd/analytics.service:9`, `Makefile:118`, `scripts/smoke.sh:33`

**Interfaces:**
- Produces: `app.Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, api, mcp bool) error`.

- [ ] **Step 1: Write the failing tests**

In `cmd/analytics/commands_test.go` add:

```go
func TestBareServeIsUsageError(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"serve"}, &out)
	if code == 0 {
		t.Fatal("bare serve did not fail")
	}
	want := "specify at least one surface: -api (ingestion), -mcp (MCP endpoint)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output %q missing %q", out.String(), want)
	}
}
```

In `internal/app/app_test.go` add (alongside the existing end-to-end serve test, reusing its env/config scaffolding):

`TestServeSharedListenerServesBothSurfaces`, written against the scaffolding the existing end-to-end test in `app_test.go` already provides (env map via `t.Setenv`, free port from `net.Listen("tcp", "127.0.0.1:0")`, `app.Serve` in a goroutine, cancel ctx and wait for return). Environment: `MCP_AUTH_MODE=token`, `MCP_TOKEN=ar_apptest`. Two sub-runs:

1. `MCP_ADDR` unset (shared listener): assert `GET /healthz` → 200, `POST /api/events` → anything but 404 (the ingest surface answers), and `POST /mcp` with no token → **401** (the MCP surface is mounted and guarded on the same port).
2. `MCP_ADDR` set to a second free port: assert `POST /mcp` on the ingest port → **404**, and on the MCP port → 401.

Also assert clean shutdown: after cancelling the context, `app.Serve` returns nil within 5s in both sub-runs (this is what pins the multi-listener shutdown ordering).

- [ ] **Step 2: Run to verify failure** — bare `serve` currently starts (then fails on config or runs); the flag error does not exist.

- [ ] **Step 3: Implement**

`cmd/analytics/serve.go`:

```go
func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	api := fs.Bool("api", false, "run the ingestion API")
	mcpFlag := fs.Bool("mcp", false, "run the MCP endpoint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*api && !*mcpFlag {
		fmt.Fprintln(stdout, "serve: specify at least one surface: -api (ingestion), -mcp (MCP endpoint)")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if *mcpFlag {
		if err := cfg.ValidateMCP(); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
	}
	logger := app.NewLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger, *api, *mcpFlag); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
```

`internal/app/app.go` — `Serve` gains `api, mcpOn bool`. Changes to the body:

- The store/registry/geo/pipeline/jobs setup runs regardless (jobs and pipeline are harmless when only MCP runs, and the MCP surface needs store+registry; the geo provider and pipeline are cheap idle). Simpler rule, fewer states: **only the HTTP surfaces are conditional.**
- Build the ingest handler only when `api`; build the MCP handler only when `mcpOn`:

```go
var mcpHandler http.Handler
var mcpClose func() error
if mcpOn {
	ops := manage.NewOps(reg, st)
	mcpHandler, mcpClose, err = mcpserver.NewHandler(ctx, cfg, reg, ops, logger)
	if err != nil {
		return err
	}
	defer mcpClose()
}
```

- Listener assembly implements the §3.2 rule:

```go
type surface struct {
	addr    string
	handler http.Handler
}
var surfaces []surface
switch {
case api && mcpOn && cfg.MCP.Addr == cfg.Listen:
	mux := http.NewServeMux()
	mux.Handle("/", ingestHandler) // ingest keeps its own /healthz and /js/*
	mcpserver.RegisterOn(mux, mcpProtected(mcpHandler), cfg, false)
	surfaces = append(surfaces, surface{cfg.Listen, mux})
case api && mcpOn:
	surfaces = append(surfaces,
		surface{cfg.Listen, ingestHandler},
		surface{cfg.MCP.Addr, mcpHandler})
case api:
	surfaces = append(surfaces, surface{cfg.Listen, ingestHandler})
default:
	surfaces = append(surfaces, surface{cfg.MCP.Addr, mcpHandler})
}
```

**Correction to the above before implementing** — `NewHandler` already returns a mux with `/mcp` + healthz mounted; the shared case must not double-wrap. Restructure `NewHandler` so the shared path is first-class instead of unwrapping: split it into

```go
func Build(ctx, cfg, reg, ops, logger) (protected http.Handler, closeDB func() error, err error) // auth-wrapped streamable handler only
```

and keep `NewHandler` = `Build` + own mux + `RegisterOn(mux, protected, cfg, true)`. The shared case calls `Build` + `RegisterOn(sharedMux, protected, cfg, false)`; the standalone case calls `NewHandler`. Adjust Task 20's file accordingly when executing this task (move three lines; `server_test.go` keeps testing `NewHandler`).

- Start every surface, fail fast on any listener error, shut all down in the existing order (HTTP first, then jobs, then pipeline):

```go
srvs := make([]*http.Server, len(surfaces))
errCh := make(chan error, len(surfaces))
for i, s := range surfaces {
	srvs[i] = &http.Server{Addr: s.addr, Handler: s.handler, ReadHeaderTimeout: 5 * time.Second}
	go func(sv *http.Server) { errCh <- sv.ListenAndServe() }(srvs[i])
	logger.Info("serving", "addr", s.addr)
}
```

and in the shutdown path, `Shutdown` each server before stopping jobs/pipeline — the existing single-server select/shutdown block generalizes by looping over `srvs`.

- The ingest-summary goroutine starts only when `api` (it reads the ingest handler's counters).

- [ ] **Step 4: Update the four call sites**

- `Dockerfile:52`: `CMD ["serve", "-api"]`
- `deploy/systemd/analytics.service:9`: `ExecStart=/usr/local/bin/analytics serve -api`
- `Makefile:118`: `./$(BIN) serve -api`
- `scripts/smoke.sh:33`: `./analytics serve -api > "$dir/log" 2>&1 &`

- [ ] **Step 5: Run everything**

Run: `go test ./... && make build && ./scripts/smoke.sh`
Expected: PASS; smoke proves the collector path end-to-end with `-api`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: serve -api/-mcp surface flags; bare serve is a usage error"
```

### Task 22: Documentation and final verification

**Files:**
- Modify: `README.md`, `docs/deployment.md`, `.env.example`, `deploy/compose/docker-compose.yml` (comment only)

- [ ] **Step 1: `.env.example`** — append:

```
# MCP endpoint (`serve -mcp`). No default auth mode: set MCP_AUTH_MODE
# to token (single MCP_TOKEN, mint with `analytics keygen -mcp`),
# oauth (external IdP: MCP_AUTH_ISSUER + MCP_RESOURCE_URL), or
# cloudflare (Access managed OAuth: MCP_CF_TEAM_DOMAIN + MCP_CF_AUD).
#MCP_ADDR=
#MCP_DB_PATH=
#MCP_AUTH_MODE=
#MCP_TOKEN=
#MCP_RESOURCE_URL=
#MCP_AUTH_ISSUER=
#MCP_AUTH_AUDIENCE=
#MCP_CF_TEAM_DOMAIN=
#MCP_CF_AUD=
#MCP_QUERY_TIMEOUT=10s
#MCP_QUERY_MAX_ROWS=1000
```

- [ ] **Step 2: README** — add an "Asking your analytics questions (MCP)" section after Embedding covering: what it is (connect Claude/Cursor, ask questions, manage projects); the three auth modes with a config block each (token: two lines; oauth: issuer+resource; cloudflare: the five setup steps from the endpoint spec discussion — Access app scoped so `/api/events` stays public, managed OAuth on, team domain + AUD tag env vars); the `serve -api -mcp` flag table from spec §3.2; and the breaking-change callout: bare `serve` now exits with the usage error, `install.sh` and compose self-heal, hand-rolled units need `-api` added. Update the Images table row for `serve` → `serve -api`.

- [ ] **Step 3: README GDPR section** — add the paragraph from endpoint spec §11: enabling `-mcp` on an instance with identified projects exposes stored user ids and display names to every holder of a valid token; the IdP (or the single token) is the whole of the access control; management tools ride the same token, with client-side approval prompts and the audit log as the operational guardrails; anonymous projects expose nothing personal. Point at `analytics project delete` as the complete-erasure lever.

- [ ] **Step 4: docs/deployment.md** — runbook rows: enabling MCP on an installed host (edit `analytics.env`, add `-mcp` to the unit or run a second unit with only `-mcp`), the Cloudflare Access arrangement, and the audit query (`sqlite3 … "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"`).

- [ ] **Step 5: Final verification**

Run: `make check && make build-all && ./scripts/smoke.sh && ./scripts/test-compose.sh`
Expected: everything green; coverage ≥85% on every core package including `internal/manage` and `internal/mcpserver`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "docs: MCP endpoint and registry configuration"
```
