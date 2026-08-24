# App Analytics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the analytics collector to ingest desktop and mobile app telemetry through a single unified `/api/events` endpoint, with per-client ingest keys, project-level identity modes, retention cohorts, and user/group dimensions.

**Architecture:** A new `app_views` raw class sits parallel to `web_hits`; custom events from every surface keep sharing `product_events`, which gains app context columns. All three are fed by one endpoint carrying a uniform `{id, ts, name, attributes}` shape where a `$` prefix marks system-defined names and keys. Authentication moves from origin-allowlist-only to per-client ingest keys that also resolve the project, so no payload carries a project field.

**Tech Stack:** Go 1.25 (stdlib + `modernc.org/sqlite` + `github.com/google/uuid`), SQLite WAL, Evidence.dev dashboards, vanilla ES5 tracking snippet.

**Spec:** `docs/superpowers/specs/2026-08-23-app-analytics-design.md`

## Global Constraints

- **Dependencies:** stdlib only, except `modernc.org/sqlite` and `github.com/google/uuid`. Adding any other module is a plan violation.
- **No CGO.** `CGO_ENABLED=0` must keep building for `linux/amd64`, `linux/arm64`, `linux/arm`.
- **Coverage gate:** `make check` requires total ≥ 80%, and ≥ 85% for `internal/store`, `internal/enrich`, `internal/pipeline`, `internal/identity`, `internal/config`. Every task ends green.
- **Timestamps:** UTC everywhere. The storage format constant is `tsFormat = "2006-01-02T15:04:05Z"` in `internal/store/sqlite/write.go`. Days are `YYYY-MM-DD` via `internal/civil`.
- **Never log** request bodies, IP addresses, User-Agents, salts, or ingest keys. Log key **labels** only.
- **Batch limits:** body ≤ 256 KB (`maxBody = 256 << 10`); ≤ 500 events per batch; ≤ 50 attributes per event; attribute keys ≤ 64 chars; values stringified at ≤ 512 chars.
- **Notice caps:** `errors` and `warnings` arrays are each capped at 10 entries per response.
- **Reserved names:** `$pageview`, `$screen_view`. Any other `$`-prefixed name is stored as an ordinary custom event **with a warning** — never rejected.
- **Reserved keys:** `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` `$platform` `$app_version` `$os_version` `$device_model` `$locale` `$url` `$referrer` `$screen`. Any other `$`-prefixed key is dropped **with a warning** — never rejected.
- **Identity modes:** `anonymous` (default) and `identified`. The server is always the enforcement point; client hints never override it.
- **`group_id` is stored raw in both modes.** `$user_name` is ignored entirely in anonymous mode.

---

### Task 1: Config — ingest keys, identity mode, app retention

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Modify: `projects.example.json`, `.env.example`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `config.IngestKey{Key, Label string; Disabled bool}`
  - `config.Project` gains `Identity string`, `IngestKeys []IngestKey`
  - `config.Retention` gains `App RetentionClass`; `config.RetentionOverride` gains `App *RetentionClassOverride`
  - `func (c *Config) ProjectByKey(key string) (*Project, string, bool)` — returns project, key label, found
  - `func (c *Config) MaxEventAge() time.Duration`
  - `func (c *Config) DisabledKeyProjects() []string` — aliases whose keys are all disabled
  - Identity constants `config.IdentityAnonymous = "anonymous"`, `config.IdentityIdentified = "identified"`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestParseProjectsIngestKeys(t *testing.T) {
	ps, err := config.ParseProjects(strings.NewReader(`[
	  {"alias":"a","name":"A","identity":"identified",
	   "ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_2","label":"ios","disabled":true}]}
	]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ps) != 1 || len(ps[0].IngestKeys) != 2 {
		t.Fatalf("got %+v", ps)
	}
	if ps[0].Identity != "identified" {
		t.Errorf("identity = %q", ps[0].Identity)
	}
	if !ps[0].IngestKeys[1].Disabled {
		t.Error("second key should be disabled")
	}
}

func TestIdentityDefaultsToAnonymous(t *testing.T) {
	c := loadWithProjects(t, `[{"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"w"}]}]`)
	if got := c.Project("a").Identity; got != config.IdentityAnonymous {
		t.Errorf("identity = %q, want anonymous", got)
	}
}

func TestRejectsUnknownIdentity(t *testing.T) {
	_, err := loadWithProjectsErr(t, `[{"alias":"a","name":"A","identity":"pseudonymous",
	  "ingest_keys":[{"key":"ak_1","label":"w"}]}]`)
	if err == nil {
		t.Fatal("want error for unknown identity mode")
	}
}

func TestRejectsProjectWithoutKeys(t *testing.T) {
	_, err := loadWithProjectsErr(t, `[{"alias":"a","name":"A"}]`)
	if err == nil {
		t.Fatal("want error when ingest_keys is missing")
	}
}

func TestRejectsDuplicateKeyAcrossProjects(t *testing.T) {
	_, err := loadWithProjectsErr(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"dup","label":"w"}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"dup","label":"w"}]}]`)
	if err == nil {
		t.Fatal("want error for duplicate key across projects")
	}
}

func TestProjectByKey(t *testing.T) {
	c := loadWithProjects(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_off","label":"old","disabled":true}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"ak_2","label":"ios"}]}]`)

	p, label, ok := c.ProjectByKey("ak_1")
	if !ok || p.Alias != "a" || label != "web" {
		t.Fatalf("ak_1 -> %v %q %v", p, label, ok)
	}
	if _, _, ok := c.ProjectByKey("ak_off"); ok {
		t.Error("disabled key must not resolve")
	}
	if _, _, ok := c.ProjectByKey("nope"); ok {
		t.Error("unknown key must not resolve")
	}
}

func TestDisabledKeyProjects(t *testing.T) {
	c := loadWithProjects(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"w","disabled":true}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"ak_2","label":"w"}]}]`)
	got := c.DisabledKeyProjects()
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("DisabledKeyProjects() = %v, want [a]", got)
	}
}

func TestAppRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c := loadWithProjects(t, `[{"alias":"a","name":"A","ingest_keys":[{"key":"k","label":"w"}]}]`)
	if c.Retention.App.RawDays != 30 || c.Retention.App.AggregateDays != 365 {
		t.Fatalf("app retention = %+v", c.Retention.App)
	}
	if want := 30 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestRetentionForAppOverride(t *testing.T) {
	c := loadWithProjects(t, `[{"alias":"a","name":"A",
	  "ingest_keys":[{"key":"k","label":"w"}],
	  "retention":{"app":{"raw_days":14}}}]`)
	r := c.RetentionFor("a")
	if r.App.RawDays != 14 || r.App.AggregateDays != 365 {
		t.Fatalf("merged app retention = %+v", r.App)
	}
}
```

Add these helpers to the same file if not already present:

```go
func loadWithProjects(t *testing.T, projects string) *config.Config {
	t.Helper()
	c, err := loadWithProjectsErr(t, projects)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func loadWithProjectsErr(t *testing.T, projects string) (*config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projects.json")
	if err := os.WriteFile(path, []byte(projects), 0o600); err != nil {
		t.Fatalf("write projects: %v", err)
	}
	env := map[string]string{
		"DATABASE_URL":  "sqlite:///tmp/x.db",
		"PROJECTS_FILE": path,
	}
	return config.FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'IngestKey|Identity|Key|AppRetention|MaxEventAge' -v`
Expected: FAIL — `ps[0].IngestKeys` undefined, `config.IdentityAnonymous` undefined.

- [ ] **Step 3: Implement**

Add `"crypto/subtle"` to the `internal/config/config.go` imports, then add
the identity constants and the key type:

```go
// Identity modes (spec §5). anonymous salts and rotates whatever identifier
// the client supplies; identified stores it as given.
const (
	IdentityAnonymous  = "anonymous"
	IdentityIdentified = "identified"
)

// IngestKey is one client credential. Multiple keys per project let a
// website, an iOS app and a desktop app be retired independently.
// Disabled rather than deleted: retirement is reversible during a botched
// rollout without regenerating and redistributing.
type IngestKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}
```

Then extend `Project`:

```go
type Project struct {
	Alias              string              `json:"alias"`
	Name               string              `json:"name"`
	Identity           string              `json:"identity"`
	IngestKeys         []IngestKey         `json:"ingest_keys"`
	AllowedOrigins     []string            `json:"allowed_origins"`
	Retention          *RetentionOverride  `json:"retention"`
	ProductAggregation *ProductAggregation `json:"product_aggregation"`
}
```

Extend retention:

```go
type Retention struct {
	Web     RetentionClass `json:"web"`
	Product RetentionClass `json:"product"`
	App     RetentionClass `json:"app"`
}

type RetentionOverride struct {
	Web     *RetentionClassOverride `json:"web"`
	Product *RetentionClassOverride `json:"product"`
	App     *RetentionClassOverride `json:"app"`
}
```

Add `App` to the `Retention` literal in `FromEnv`:

```go
			App: RetentionClass{
				RawDays:       e.num("RETENTION_APP_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_APP_AGGREGATE_DAYS", 365),
			},
```

Add the key index to `Config` and build it in `applyDefaults`:

```go
type Config struct {
	Listen    string
	Database  string
	Geo       string
	Log       LogConfig
	Buffer    BufferConfig
	Retention Retention
	Sync      SyncConfig
	Projects  []Project

	// keys is a flat list of non-disabled ingest keys. A slice rather than
	// a map because lookup uses subtle.ConstantTimeCompare: a map index on
	// a credential is not constant time.
	keys []keyOwner
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}
```

In `applyDefaults`:

```go
func (c *Config) applyDefaults() {
	c.keys = nil
	for i := range c.Projects {
		p := &c.Projects[i]
		if pa := p.ProductAggregation; pa != nil && pa.TopN == 0 {
			pa.TopN = 50
		}
		if p.Identity == "" {
			p.Identity = IdentityAnonymous
		}
		for _, k := range p.IngestKeys {
			if !k.Disabled {
				c.keys = append(c.keys, keyOwner{key: k.Key, project: p, label: k.Label})
			}
		}
	}
}
```

In `validate`, inside the per-project loop, after the alias checks:

```go
		switch p.Identity {
		case IdentityAnonymous, IdentityIdentified:
		default:
			return fmt.Errorf("config: project %q: identity must be %q or %q, got %q",
				p.Alias, IdentityAnonymous, IdentityIdentified, p.Identity)
		}
		if len(p.IngestKeys) == 0 {
			return fmt.Errorf("config: project %q has no ingest_keys; run `analytics keygen`", p.Alias)
		}
		for _, k := range p.IngestKeys {
			if k.Key == "" {
				return fmt.Errorf("config: project %q has an empty ingest key", p.Alias)
			}
			if owner, dup := seenKey[k.Key]; dup {
				return fmt.Errorf("config: ingest key of project %q is already used by project %q; keys must be globally unique", p.Alias, owner)
			}
			seenKey[k.Key] = p.Alias
		}
```

Declare `seenKey := map[string]string{}` next to `seen := map[string]bool{}`. Add `App` to the negative-days loop and to the merged-retention check:

```go
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product, c.Retention.App} {
```

```go
		if r.Web.RawDays < 0 || r.Web.AggregateDays < 0 ||
			r.Product.RawDays < 0 || r.Product.AggregateDays < 0 ||
			r.App.RawDays < 0 || r.App.AggregateDays < 0 {
			return fmt.Errorf("config: project %q has invalid merged retention (negative values): web=%+v product=%+v app=%+v", p.Alias, r.Web, r.Product, r.App)
		}
```

Add `apply(&r.App, p.Retention.App)` in `RetentionFor`, and the new accessors:

```go
// ProjectByKey resolves a project from an ingest key. Disabled keys never
// resolve. The boolean is the only auth outcome: the key resolves or it
// does not.
func (c *Config) ProjectByKey(key string) (*Project, string, bool) {
	// Every candidate is compared, with no early return on match, so the
	// loop's timing does not vary with key content. Length differences do
	// leak through ConstantTimeCompare's zero return; that is unavoidable
	// and harmless for 128-bit random keys.
	match := -1
	kb := []byte(key)
	for i := range c.keys {
		if subtle.ConstantTimeCompare([]byte(c.keys[i].key), kb) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, "", false
	}
	return c.keys[match].project, c.keys[match].label, true
}

// MaxEventAge is derived from the app raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in an
// already-aggregated day.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.App.RawDays) * 24 * time.Hour
}

// DisabledKeyProjects lists projects that can receive nothing because all
// their keys are disabled. That is a legitimate retired state, so callers
// warn rather than fail.
func (c *Config) DisabledKeyProjects() []string {
	var out []string
	for i := range c.Projects {
		p := &c.Projects[i]
		active := false
		for _, k := range p.IngestKeys {
			if !k.Disabled {
				active = true
				break
			}
		}
		if !active {
			out = append(out, p.Alias)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS. Existing fixtures that omit `ingest_keys` will now fail validation — update every projects fixture in `internal/config/`, `internal/config/configtest/`, and `internal/server/server_test.go` to include `"ingest_keys":[{"key":"ak_test","label":"test"}]`.

- [ ] **Step 5: Update the shipped examples**

`projects.example.json`:

```json
[
  {
    "alias": "myapp",
    "name": "My App",
    "identity": "anonymous",
    "ingest_keys": [
      { "key": "REPLACE_ME_RUN_ANALYTICS_KEYGEN", "label": "web" }
    ],
    "allowed_origins": ["https://myapp.com"]
  }
]
```

Append to `.env.example` after the product retention block:

```
#RETENTION_APP_RAW_DAYS=30
#RETENTION_APP_AGGREGATE_DAYS=365
```

- [ ] **Step 6: Run the full gate and commit**

Run: `make check`
Expected: PASS, `internal/config` ≥ 85%.

```bash
git add internal/config projects.example.json .env.example internal/server/server_test.go
git commit -m "feat(config): ingest keys, identity mode, app retention"
```

---

### Task 2: Migration 003/004 — schema and the actor_id rename

**Files:**
- Create: `internal/store/sqlite/migrations/003_app.sql`
- Create: `internal/store/sqlite/migrations/004_app_views.sql`
- Modify: `internal/store/sqlite/migrations/002_views.sql` — no edit; it is superseded by 004 for renamed columns (see Step 3 note)
- Modify: `internal/store/sqlite/write.go`, `aggregate_web.go`, `aggregate_product.go`, `flatview.go`
- Test: `internal/store/sqlite/sqlite_test.go`

**Interfaces:**
- Consumes: Task 1 config types (not directly referenced here)
- Produces: tables `app_views`, `agg_app_daily`, `agg_app_screens`, `agg_app_versions`, `agg_app_os`, `agg_app_devices`, `agg_app_countries`, `actors`, `agg_retention`, `identities`, `agg_identity_daily`; columns `web_hits.actor_id/user_id/group_id/received_at`, `product_events.actor_id/user_id/group_id/platform/app_version/received_at`, `projects.identity`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite/sqlite_test.go`:

```go
func TestMigration003Schema(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for _, table := range []string{
		"app_views", "agg_app_daily", "agg_app_screens", "agg_app_versions",
		"agg_app_os", "agg_app_devices", "agg_app_countries",
		"actors", "agg_retention", "identities", "agg_identity_daily",
	} {
		var n int
		if err := db.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}

	for _, c := range []struct{ table, column string }{
		{"web_hits", "actor_id"}, {"web_hits", "user_id"},
		{"web_hits", "group_id"}, {"web_hits", "received_at"},
		{"product_events", "actor_id"}, {"product_events", "user_id"},
		{"product_events", "group_id"}, {"product_events", "platform"},
		{"product_events", "app_version"}, {"product_events", "received_at"},
		{"projects", "identity"},
	} {
		if !hasColumn(t, db, c.table, c.column) {
			t.Errorf("%s.%s missing", c.table, c.column)
		}
	}
	if hasColumn(t, db, "web_hits", "visitor_hash") {
		t.Error("web_hits.visitor_hash should have been renamed to actor_id")
	}
}

func hasColumn(t *testing.T, db *sqlite.DB, table, column string) bool {
	t.Helper()
	rows, err := db.DB().QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
```

If `openTestDB` does not already exist in the package, add it; if `DB.DB()` (raw handle accessor) does not exist, add it to `sqlite.go`:

```go
// DB exposes the raw handle for tests and for the view rebuilder.
func (d *DB) DB() *sql.DB { return d.db }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestMigration003Schema -v`
Expected: FAIL — `table app_views missing`.

- [ ] **Step 3: Write migration 003**

Create `internal/store/sqlite/migrations/003_app.sql`. The views from 002 reference `visitor_hash` and `user_id`; SQLite refuses to rename a column that a view depends on unless `legacy_alter_table` is off and the view can be reparsed, so drop the views first and let 004 recreate them.

```sql
-- App analytics (spec 2026-08-23 §6). Views are dropped here and recreated
-- by 004 because RENAME COLUMN must not leave a stale view definition.

DROP VIEW IF EXISTS v_web_daily;
DROP VIEW IF EXISTS v_web_pages;
DROP VIEW IF EXISTS v_web_referrers;
DROP VIEW IF EXISTS v_web_countries;
DROP VIEW IF EXISTS v_web_devices;
DROP VIEW IF EXISTS v_web_browsers;
DROP VIEW IF EXISTS v_web_os;
DROP VIEW IF EXISTS v_web_utm;
DROP VIEW IF EXISTS v_product_daily;
DROP VIEW IF EXISTS v_product_totals;
DROP VIEW IF EXISTS v_events_flat;

ALTER TABLE projects ADD COLUMN identity TEXT NOT NULL DEFAULT 'anonymous';

-- ===== web_hits: rename + identity columns =====
ALTER TABLE web_hits RENAME COLUMN visitor_hash TO actor_id;
ALTER TABLE web_hits ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE web_hits ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== product_events: rename + identity and app context =====
ALTER TABLE product_events RENAME COLUMN user_id TO actor_id;
ALTER TABLE product_events ADD COLUMN user_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN group_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN platform    TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE product_events ADD COLUMN received_at TEXT NOT NULL DEFAULT '';

-- ===== raw: app views =====
CREATE TABLE app_views (
    id           TEXT PRIMARY KEY,
    project      TEXT NOT NULL,
    ts           TEXT NOT NULL,
    received_at  TEXT NOT NULL,
    actor_id     TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
    group_id     TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    screen       TEXT NOT NULL,
    platform     TEXT NOT NULL DEFAULT '',
    app_version  TEXT NOT NULL DEFAULT '',
    os_version   TEXT NOT NULL DEFAULT '',
    device_model TEXT NOT NULL DEFAULT '',
    locale       TEXT NOT NULL DEFAULT '',
    country      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_app_views_project_ts ON app_views(project, ts);
CREATE INDEX idx_app_views_actor      ON app_views(project, actor_id, ts);
CREATE INDEX idx_app_views_session    ON app_views(project, session_id, ts);

-- ===== app aggregates =====
CREATE TABLE agg_app_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    sessions INTEGER NOT NULL, duration_sec INTEGER NOT NULL,
    PRIMARY KEY (project, day)
) WITHOUT ROWID;

CREATE TABLE agg_app_screens (
    project TEXT NOT NULL, day TEXT NOT NULL, screen TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, screen)
) WITHOUT ROWID;

-- platform is in the key: "2.4.1" means unrelated things across iOS and
-- Android, and without it the rollup silently merges separate releases.
CREATE TABLE agg_app_versions (
    project TEXT NOT NULL, day TEXT NOT NULL,
    platform TEXT NOT NULL, app_version TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform, app_version)
) WITHOUT ROWID;

CREATE TABLE agg_app_os (
    project TEXT NOT NULL, day TEXT NOT NULL,
    platform TEXT NOT NULL, os_version TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, platform, os_version)
) WITHOUT ROWID;

CREATE TABLE agg_app_devices (
    project TEXT NOT NULL, day TEXT NOT NULL, device_model TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, device_model)
) WITHOUT ROWID;

CREATE TABLE agg_app_countries (
    project TEXT NOT NULL, day TEXT NOT NULL, country TEXT NOT NULL,
    actives INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project, day, country)
) WITHOUT ROWID;

-- ===== cohorts =====
CREATE TABLE actors (
    project TEXT NOT NULL, actor_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    first_seen_day TEXT NOT NULL,
    last_seen_day  TEXT NOT NULL,
    PRIMARY KEY (project, actor_id)
) WITHOUT ROWID;
CREATE INDEX idx_actors_last_seen ON actors(project, last_seen_day);

CREATE TABLE agg_retention (
    project TEXT NOT NULL, surface TEXT NOT NULL,
    cohort_day TEXT NOT NULL, day_offset INTEGER NOT NULL,
    actors INTEGER NOT NULL,
    PRIMARY KEY (project, surface, cohort_day, day_offset)
) WITHOUT ROWID;

-- ===== identities and their aggregates =====
CREATE TABLE identities (
    project       TEXT NOT NULL,
    kind          TEXT NOT NULL,
    id            TEXT NOT NULL,
    name          TEXT NOT NULL,
    last_seen_day TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (project, kind, id)
) WITHOUT ROWID;

CREATE TABLE agg_identity_daily (
    project TEXT NOT NULL, day TEXT NOT NULL,
    kind TEXT NOT NULL, id TEXT NOT NULL,
    actors INTEGER NOT NULL, users INTEGER NOT NULL,
    hits INTEGER NOT NULL, views INTEGER NOT NULL, events INTEGER NOT NULL,
    PRIMARY KEY (project, day, kind, id)
) WITHOUT ROWID;
```

- [ ] **Step 4: Write migration 004**

Create `internal/store/sqlite/migrations/004_app_views.sql` by copying the whole body of `002_views.sql` and applying exactly two textual substitutions, then appending the new app views. Do the copy mechanically:

```bash
sed -e 's/\bvisitor_hash\b/actor_id/g' \
    -e 's/CREATE VIEW /CREATE VIEW IF NOT EXISTS /g' \
    internal/store/sqlite/migrations/002_views.sql \
    > internal/store/sqlite/migrations/004_app_views.sql
```

`product_events.user_id` in 002 refers to the actor, so also rewrite those references. Inspect the generated file and replace `user_id` with `actor_id` **only** inside `FROM product_events` sub-selects (the aggregate tables `agg_product_*` have no `user_id` column, so there should be no other occurrences).

Then append the app stitch views to `004_app_views.sql`:

```sql

-- ===== app stitch views (spec §6.4) =====
-- Each: aggregate table UNION ALL the same shape computed live from raw
-- rows for days not yet aggregated. The live halves must stay numerically
-- identical to aggregate_app.go; views_test.go enforces that.

CREATE VIEW IF NOT EXISTS v_app_daily AS
SELECT project, day, actives, views, sessions, duration_sec FROM agg_app_daily
UNION ALL
SELECT c.project, c.day, c.actives, c.views, s.sessions, s.duration_sec
FROM (
  SELECT project, substr(ts,1,10) AS day,
         COUNT(DISTINCT actor_id) AS actives, COUNT(*) AS views
  FROM app_views GROUP BY project, substr(ts,1,10)
) c
JOIN (
  WITH base AS (
    SELECT project, substr(ts,1,10) AS day, actor_id,
           CASE WHEN session_id <> '' THEN session_id ELSE '' END AS sid,
           CAST(strftime('%s', ts) AS INTEGER) AS t
    FROM app_views
  ),
  marked AS (
    SELECT project, day, actor_id, sid, t,
           CASE WHEN sid <> '' THEN 0
                WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
                ELSE 0 END AS new_session
    FROM base WINDOW w AS (PARTITION BY project, day, actor_id ORDER BY t)
  ),
  numbered AS (
    SELECT project, day, actor_id, t,
           CASE WHEN sid <> '' THEN sid
                ELSE CAST(SUM(new_session) OVER (PARTITION BY project, day, actor_id ORDER BY t) AS TEXT)
           END AS skey
    FROM marked
  ),
  spans AS (
    SELECT project, day, actor_id, skey, MAX(t) - MIN(t) AS dur
    FROM numbered GROUP BY project, day, actor_id, skey
  )
  SELECT project, day, COUNT(*) AS sessions, SUM(dur) AS duration_sec
  FROM spans GROUP BY project, day
) s ON s.project = c.project AND s.day = c.day;

CREATE VIEW IF NOT EXISTS v_app_screens AS
SELECT project, day, screen, actives, views FROM agg_app_screens
UNION ALL
SELECT project, substr(ts,1,10), screen,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), screen;

CREATE VIEW IF NOT EXISTS v_app_versions AS
SELECT project, day, platform, app_version, actives, views FROM agg_app_versions
UNION ALL
SELECT project, substr(ts,1,10), platform, app_version,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), platform, app_version;

CREATE VIEW IF NOT EXISTS v_app_os AS
SELECT project, day, platform, os_version, actives, views FROM agg_app_os
UNION ALL
SELECT project, substr(ts,1,10), platform, os_version,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), platform, os_version;

CREATE VIEW IF NOT EXISTS v_app_devices AS
SELECT project, day, device_model, actives, views FROM agg_app_devices
UNION ALL
SELECT project, substr(ts,1,10), device_model,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), device_model;

CREATE VIEW IF NOT EXISTS v_app_countries AS
SELECT project, day, country, actives, views FROM agg_app_countries
UNION ALL
SELECT project, substr(ts,1,10), country,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM app_views GROUP BY project, substr(ts,1,10), country;

CREATE VIEW IF NOT EXISTS v_identity_daily AS
SELECT project, day, kind, id, actors, users, hits, views, events
FROM agg_identity_daily;

-- Retention is defined only over aggregated days, so no live half.
-- cohort_size is the offset-0 row, exposed for rate computation.
CREATE VIEW IF NOT EXISTS v_retention AS
SELECT r.project, r.surface, r.cohort_day, r.day_offset, r.actors,
       c.actors AS cohort_size
FROM agg_retention r
JOIN agg_retention c
  ON c.project = r.project AND c.surface = r.surface
 AND c.cohort_day = r.cohort_day AND c.day_offset = 0;
```

- [ ] **Step 5: Update Go SQL that references the renamed columns**

`internal/store/sqlite/write.go` — `WriteWebHits` column list `visitor_hash` becomes `actor_id`; `WriteProductEvents` `user_id` becomes `actor_id`. (Task 3 rewrites both statements fully; for now make the minimal rename so the package compiles.)

`internal/store/sqlite/aggregate_web.go` — replace every `visitor_hash` with `actor_id`.

`internal/store/sqlite/aggregate_product.go` — replace every `product_events.user_id` / bare `user_id` that means the actor with `actor_id`.

`internal/store/sqlite/flatview.go` — the flat view's base columns list `user_id`; rename to `actor_id`.

Run: `grep -rn 'visitor_hash' internal/ --include=*.go` — expected: only `internal/identity/identity.go` (the `VisitorHash` function name, which stays).

- [ ] **Step 6: Run the store tests**

Run: `go test ./internal/store/... -v`
Expected: PASS. Fix any test fixture that inserts `visitor_hash` or reads `user_id` from `product_events`.

- [ ] **Step 7: Commit**

```bash
git add internal/store/sqlite
git commit -m "feat(store): migration 003/004 — app schema and actor_id rename"
```

---

### Task 3: Store models and writes

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/sqlite/write.go`
- Test: `internal/store/sqlite/write_test.go`

**Interfaces:**
- Consumes: Task 2 schema
- Produces:
  - `store.AppView` struct (fields below)
  - `store.WebHit` gains `ActorID, UserID, GroupID string` and `ReceivedAt time.Time`; loses `VisitorHash`
  - `store.ProductEvent` gains `ActorID, UserID, GroupID, Platform, AppVersion string` and `ReceivedAt time.Time`; loses `UserID`-as-actor meaning
  - `store.Identity{Project, Kind, ID, Name string}`
  - `Store.WriteAppViews(ctx context.Context, views []AppView) error`
  - `Store.UpsertIdentities(ctx context.Context, ids []Identity) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/sqlite/write_test.go`:

```go
func TestWriteAppViewsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	in := []store.AppView{{
		ID: "018f-a", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "act1", UserID: "u1", GroupID: "org9", SessionID: "s1",
		Screen: "/settings", Platform: "ios", AppVersion: "2.4.1",
		OSVersion: "17.2", DeviceModel: "iPhone15,2", Locale: "en-US", Country: "DE",
	}}
	if err := db.WriteAppViews(ctx, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	var screen, platform, group string
	if err := db.DB().QueryRowContext(ctx,
		`SELECT screen, platform, group_id FROM app_views WHERE id=?`, "018f-a").
		Scan(&screen, &platform, &group); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if screen != "/settings" || platform != "ios" || group != "org9" {
		t.Errorf("got %q %q %q", screen, platform, group)
	}
}

func TestWritesAreIdempotentOnID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	view := store.AppView{ID: "dup", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "a", Screen: "/x"}
	hit := store.WebHit{ID: "duph", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "a", Path: "/x"}
	ev := store.ProductEvent{ID: "dupe", Project: "p", EventName: "n",
		TS: ts, ReceivedAt: ts, ActorID: "a"}

	for i := 0; i < 2; i++ {
		if err := db.WriteAppViews(ctx, []store.AppView{view}); err != nil {
			t.Fatalf("app write %d: %v", i, err)
		}
		if err := db.WriteWebHits(ctx, []store.WebHit{hit}); err != nil {
			t.Fatalf("hit write %d: %v", i, err)
		}
		if err := db.WriteProductEvents(ctx, []store.ProductEvent{ev}); err != nil {
			t.Fatalf("event write %d: %v", i, err)
		}
	}

	for _, table := range []string{"app_views", "web_hits", "product_events"} {
		var n int
		if err := db.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows after replay, want 1", table, n)
		}
	}
}

func TestUpsertIdentitiesLatestNameWins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: "user", ID: "u1", Name: "Ada"},
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: "user", ID: "u1", Name: "Ada Lovelace"},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var name string
	var n int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx,
		`SELECT name FROM identities WHERE project='p' AND kind='user' AND id='u1'`).
		Scan(&name); err != nil {
		t.Fatal(err)
	}
	if n != 1 || name != "Ada Lovelace" {
		t.Errorf("rows=%d name=%q, want 1 and latest", n, name)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run 'AppViews|Idempotent|Identities' -v`
Expected: FAIL — `store.AppView` undefined.

- [ ] **Step 3: Update the models**

In `internal/store/store.go`:

```go
// WebHit represents a single web pageview ($pageview).
type WebHit struct {
	ID, Project                       string
	TS, ReceivedAt                    time.Time
	ActorID, UserID, GroupID          string
	Path, ReferrerSource              string
	UTMSource, UTMMedium, UTMCampaign string
	Country, Device, Browser, OS      string
}

// ProductEvent represents a custom event from any surface.
type ProductEvent struct {
	ID, Project, EventName   string
	TS, ReceivedAt           time.Time
	ActorID, UserID, GroupID string
	Platform, AppVersion     string
	Attributes               map[string]string
}

// AppView represents a single app screen view ($screen_view). No browser,
// device class, referrer or utm columns: apps declare their context rather
// than having it parsed from a User-Agent.
type AppView struct {
	ID, Project                         string
	TS, ReceivedAt                      time.Time
	ActorID, UserID, GroupID, SessionID string
	Screen                              string
	Platform, AppVersion                string
	OSVersion, DeviceModel, Locale      string
	Country                             string
}

// Identity is a display name for a user or group, stored once and joined
// at query time: a name repeated on every event row could never be updated.
type Identity struct {
	Project, Kind, ID, Name string
}

// Identity kinds.
const (
	KindUser  = "user"
	KindGroup = "group"
)
```

Add to the `Store` interface:

```go
	WriteAppViews(ctx context.Context, views []AppView) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
```

- [ ] **Step 4: Update the write path**

In `internal/store/sqlite/write.go`, replace `WriteWebHits` and `WriteProductEvents` and add the two new methods. Note `INSERT OR IGNORE`: client-supplied UUIDv7 ids make a replayed batch a no-op.

```go
func (d *DB) WriteWebHits(ctx context.Context, hits []store.WebHit) error {
	if len(hits) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO web_hits
			(id, project, ts, received_at, actor_id, user_id, group_id,
			 path, referrer_source, utm_source, utm_medium, utm_campaign,
			 country, device, browser, os)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hits {
			if _, err := stmt.ExecContext(ctx, h.ID, h.Project,
				h.TS.UTC().Format(tsFormat), h.ReceivedAt.UTC().Format(tsFormat),
				h.ActorID, h.UserID, h.GroupID,
				h.Path, h.ReferrerSource, h.UTMSource, h.UTMMedium, h.UTMCampaign,
				h.Country, h.Device, h.Browser, h.OS); err != nil {
				return fmt.Errorf("web hit %s: %w", h.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO product_events
			(id, project, event_name, ts, received_at, actor_id, user_id, group_id,
			 platform, app_version, attributes)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range evs {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("event %s attributes: %w", e.ID, err)
			}
			if _, err := stmt.ExecContext(ctx, e.ID, e.Project, e.EventName,
				e.TS.UTC().Format(tsFormat), e.ReceivedAt.UTC().Format(tsFormat),
				e.ActorID, e.UserID, e.GroupID, e.Platform, e.AppVersion,
				string(blob)); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) WriteAppViews(ctx context.Context, views []store.AppView) error {
	if len(views) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO app_views
			(id, project, ts, received_at, actor_id, user_id, group_id, session_id,
			 screen, platform, app_version, os_version, device_model, locale, country)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, v := range views {
			if _, err := stmt.ExecContext(ctx, v.ID, v.Project,
				v.TS.UTC().Format(tsFormat), v.ReceivedAt.UTC().Format(tsFormat),
				v.ActorID, v.UserID, v.GroupID, v.SessionID,
				v.Screen, v.Platform, v.AppVersion, v.OSVersion,
				v.DeviceModel, v.Locale, v.Country); err != nil {
				return fmt.Errorf("app view %s: %w", v.ID, err)
			}
		}
		return nil
	})
}

// UpsertIdentities records display names, latest write wins. last_seen_day
// is maintained by the daily pass, not here.
func (d *DB) UpsertIdentities(ctx context.Context, ids []store.Identity) error {
	if len(ids) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO identities
			(project, kind, id, name, updated_at)
			VALUES (?,?,?,?,datetime('now'))
			ON CONFLICT(project, kind, id) DO UPDATE SET
			  name=excluded.name, updated_at=excluded.updated_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, i := range ids {
			if i.ID == "" || i.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, i.Project, i.Kind, i.ID, i.Name); err != nil {
				return fmt.Errorf("identity %s/%s: %w", i.Kind, i.ID, err)
			}
		}
		return nil
	})
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/store/... -v`
Expected: PASS. Compilation errors elsewhere (`internal/server`, `internal/pipeline`, `internal/jobs`) are expected at this point and are fixed in Tasks 8–12; use `go build ./internal/store/...` to scope.

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): AppView model, identity columns, idempotent writes"
```

---

### Task 4: App day aggregation

**Files:**
- Create: `internal/store/sqlite/aggregate_app.go`
- Test: `internal/store/sqlite/aggregate_app_test.go`
- Modify: `internal/store/store.go` (interface)

**Interfaces:**
- Consumes: Task 3 `store.AppView`
- Produces:
  - `Store.AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)`
  - `Store.AggregateAppDay(ctx context.Context, project string, day civil.Date) error`
  - package constant `topNDimension = 500`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/sqlite/aggregate_app_test.go`:

```go
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

func seedViews(t *testing.T, db *sqlite.DB, views ...store.AppView) {
	t.Helper()
	if err := db.WriteAppViews(context.Background(), views); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func at(h, m int) time.Time {
	return time.Date(2026, 8, 23, h, m, 0, 0, time.UTC)
}

func TestAggregateAppDayCounts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: 8, Day: 23}

	seedViews(t, db,
		store.AppView{ID: "1", Project: "p", TS: at(10, 0), ReceivedAt: at(10, 0),
			ActorID: "a", SessionID: "s1", Screen: "/home", Platform: "ios",
			AppVersion: "2.4.1", OSVersion: "17.2", DeviceModel: "iPhone15,2", Country: "DE"},
		store.AppView{ID: "2", Project: "p", TS: at(10, 5), ReceivedAt: at(10, 5),
			ActorID: "a", SessionID: "s1", Screen: "/settings", Platform: "ios",
			AppVersion: "2.4.1", OSVersion: "17.2", DeviceModel: "iPhone15,2", Country: "DE"},
		store.AppView{ID: "3", Project: "p", TS: at(11, 0), ReceivedAt: at(11, 0),
			ActorID: "b", SessionID: "s2", Screen: "/home", Platform: "android",
			AppVersion: "2.4.1", OSVersion: "14", DeviceModel: "Pixel 8", Country: "FR"},
	)

	if err := db.AggregateAppDay(ctx, "p", day); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	var actives, views, sessions, dur int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT actives, views, sessions, duration_sec FROM agg_app_daily WHERE project='p' AND day=?`,
		day.String()).Scan(&actives, &views, &sessions, &dur); err != nil {
		t.Fatalf("read daily: %v", err)
	}
	if actives != 2 || views != 3 || sessions != 2 || dur != 300 {
		t.Errorf("daily = actives %d views %d sessions %d dur %d; want 2 3 2 300",
			actives, views, sessions, dur)
	}

	var n int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT views FROM agg_app_screens WHERE project='p' AND day=? AND screen='/home'`,
		day.String()).Scan(&n); err != nil {
		t.Fatalf("read screens: %v", err)
	}
	if n != 2 {
		t.Errorf("/home views = %d, want 2", n)
	}

	// platform is part of the versions key: two rows for one version string.
	if err := db.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agg_app_versions WHERE project='p' AND day=? AND app_version='2.4.1'`,
		day.String()).Scan(&n); err != nil {
		t.Fatalf("read versions: %v", err)
	}
	if n != 2 {
		t.Errorf("version rows = %d, want 2 (one per platform)", n)
	}
}

func TestAggregateAppDayIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: 8, Day: 23}

	seedViews(t, db, store.AppView{ID: "1", Project: "p", TS: at(10, 0),
		ReceivedAt: at(10, 0), ActorID: "a", Screen: "/home", Platform: "ios"})

	if err := db.AggregateAppDay(ctx, "p", day); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateAppDay(ctx, "p", day); err != nil {
		t.Fatal(err)
	}

	var views int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT views FROM agg_app_daily WHERE project='p' AND day=?`, day.String()).
		Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 1 {
		t.Errorf("views = %d after double aggregation, want 1", views)
	}
}

func TestAggregateAppDaySessionFallsBackToGapInference(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: 8, Day: 23}

	// No session_id: a gap > 30 min splits sessions.
	seedViews(t, db,
		store.AppView{ID: "1", Project: "p", TS: at(10, 0), ReceivedAt: at(10, 0),
			ActorID: "a", Screen: "/a"},
		store.AppView{ID: "2", Project: "p", TS: at(11, 0), ReceivedAt: at(11, 0),
			ActorID: "a", Screen: "/b"},
	)
	if err := db.AggregateAppDay(ctx, "p", day); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT sessions FROM agg_app_daily WHERE project='p' AND day=?`, day.String()).
		Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 {
		t.Errorf("sessions = %d, want 2 from gap inference", sessions)
	}
}

func TestAppDaysBefore(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	seedViews(t, db,
		store.AppView{ID: "1", Project: "p", TS: old, ReceivedAt: old, ActorID: "a", Screen: "/x"},
		store.AppView{ID: "2", Project: "p", TS: at(10, 0), ReceivedAt: at(10, 0), ActorID: "a", Screen: "/x"},
	)

	days, err := db.AppDaysBefore(ctx, "p", civil.Date{Year: 2026, Month: 8, Day: 23})
	if err != nil {
		t.Fatalf("AppDaysBefore: %v", err)
	}
	if len(days) != 1 || days[0].String() != "2026-08-01" {
		t.Fatalf("days = %v, want [2026-08-01]", days)
	}
}
```

`openTestDB` returns `*sqlite.DB`; import `"github.com/dmitry/analytics/internal/store/sqlite"` in this test file.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run AggregateApp -v`
Expected: FAIL — `db.AggregateAppDay` undefined.

- [ ] **Step 3: Implement**

Create `internal/store/sqlite/aggregate_app.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// topNDimension caps client-supplied free-string dimensions per day; the
// tail collapses into "(other)". Web's path stays uncapped (existing
// behaviour) but an unbounded new dimension is the wrong default on
// Pi-class hardware (base spec §12a).
const topNDimension = 500

const otherBucket = "(other)"

// AppDaysBefore lists days with raw app_views strictly before the cutoff.
func (d *DB) AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, `SELECT DISTINCT substr(ts,1,10) FROM app_views
		WHERE project=? AND substr(ts,1,10) < ? ORDER BY 1`, project, before)
}

// AggregateAppDay rolls one day of app_views into the agg_app_* family and
// deletes that day's raw rows, in one transaction. Idempotent by
// construction: every write is INSERT OR REPLACE keyed on (project, day, …)
// and recomputed wholly from the raw rows the same statement then removes.
func (d *DB) AggregateAppDay(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		args := []any{project, day.String()}

		// Sessions: client session_id is authoritative; absent, a gap of
		// more than 30 minutes over actor_id splits sessions.
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO agg_app_daily (project, day, actives, views, sessions, duration_sec)
WITH src AS (
  SELECT actor_id, session_id, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM app_views WHERE project=? AND substr(ts,1,10)=?
),
marked AS (
  SELECT actor_id, session_id, t,
         CASE WHEN session_id <> '' THEN 0
              WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1
              ELSE 0 END AS new_session
  FROM src WINDOW w AS (PARTITION BY actor_id ORDER BY t)
),
keyed AS (
  SELECT actor_id, t,
         CASE WHEN session_id <> '' THEN session_id
              ELSE CAST(SUM(new_session) OVER (PARTITION BY actor_id ORDER BY t) AS TEXT)
         END AS skey
  FROM marked
),
spans AS (
  SELECT actor_id, skey, MAX(t) - MIN(t) AS dur FROM keyed GROUP BY actor_id, skey
)
SELECT ?, ?,
       (SELECT COUNT(DISTINCT actor_id) FROM src),
       (SELECT COUNT(*) FROM src),
       (SELECT COUNT(*) FROM spans),
       (SELECT COALESCE(SUM(dur), 0) FROM spans)`,
			project, day.String(), project, day.String()); err != nil {
			return fmt.Errorf("agg_app_daily: %w", err)
		}

		type dim struct {
			table string
			cols  string // dimension columns, comma separated
		}
		for _, dm := range []dim{
			{"agg_app_screens", "screen"},
			{"agg_app_versions", "platform, app_version"},
			{"agg_app_os", "platform, os_version"},
			{"agg_app_devices", "device_model"},
			{"agg_app_countries", "country"},
		} {
			if err := aggAppDimension(ctx, tx, dm.table, dm.cols, args); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM app_views WHERE project=? AND substr(ts,1,10)=?`,
			project, day.String()); err != nil {
			return fmt.Errorf("prune raw app_views: %w", err)
		}
		return nil
	})
}

// aggAppDimension rolls one dimension, keeping the top N by views and
// collapsing the tail into "(other)". The collapsed row's actives is the
// distinct actor count across the whole tail, not a sum, so it stays a
// count of people rather than of appearances.
func aggAppDimension(ctx context.Context, tx *sql.Tx, table, cols string, args []any) error {
	q := fmt.Sprintf(`
INSERT OR REPLACE INTO %[1]s (project, day, %[2]s, actives, views)
WITH src AS (
  SELECT %[2]s, actor_id FROM app_views WHERE project=? AND substr(ts,1,10)=?
),
ranked AS (
  SELECT %[2]s, COUNT(DISTINCT actor_id) AS actives, COUNT(*) AS views,
         ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, %[2]s) AS rn
  FROM src GROUP BY %[2]s
)
SELECT ?, ?, %[2]s, actives, views FROM ranked WHERE rn <= %[3]d`,
		table, cols, topNDimension)
	if _, err := tx.ExecContext(ctx, q, args[0], args[1], args[0], args[1]); err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}
```

If `daysBefore` does not already exist as a shared helper in this package, add it next to the existing `WebDaysBefore`/`ProductDaysBefore` implementations, or copy their body pattern verbatim.

Add to the `Store` interface in `internal/store/store.go`:

```go
	AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateAppDay(ctx context.Context, project string, day civil.Date) error
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/sqlite/ -run AggregateApp -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): app day aggregation with top-N dimension caps"
```

---

### Task 5: Retention cohorts

**Files:**
- Create: `internal/store/sqlite/retention.go`
- Test: `internal/store/sqlite/retention_test.go`
- Modify: `internal/store/store.go` (interface)

**Interfaces:**
- Consumes: Tasks 3–4
- Produces:
  - `Store.UpsertActors(ctx context.Context, project string, day civil.Date) error`
  - `Store.AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error`
  - `Store.PruneActors(ctx context.Context, project string, before civil.Date) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/sqlite/retention_test.go`:

```go
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

func onDay(y, m, d int) civil.Date { return civil.Date{Year: y, Month: m, Day: d} }

func viewOn(id, actor string, t time.Time) store.AppView {
	return store.AppView{ID: id, Project: "p", TS: t, ReceivedAt: t,
		ActorID: actor, Screen: "/x", Platform: "ios"}
}

func TestUpsertActorsTracksFirstAndLastSeen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	d1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewOn("1", "a", d1)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatalf("upsert day 1: %v", err)
	}
	if err := db.WriteAppViews(ctx, []store.AppView{viewOn("2", "a", d2)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatalf("upsert day 8: %v", err)
	}

	var first, last, surface string
	if err := db.DB().QueryRowContext(ctx,
		`SELECT first_seen_day, last_seen_day, surface FROM actors WHERE project='p' AND actor_id='a'`).
		Scan(&first, &last, &surface); err != nil {
		t.Fatalf("read actor: %v", err)
	}
	if first != "2026-08-01" || last != "2026-08-08" || surface != "app" {
		t.Errorf("actor = %q %q %q; want 2026-08-01 2026-08-08 app", first, last, surface)
	}
}

func TestAggregateRetentionDayComputesOffsets(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	// Two actors on day 0; one of them returns on day 7.
	if err := db.WriteAppViews(ctx, []store.AppView{
		viewOn("1", "a", cohort), viewOn("2", "b", cohort),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}

	if err := db.WriteAppViews(ctx, []store.AppView{viewOn("3", "a", later)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}

	var d0, d7 int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project='p' AND surface='app'
		   AND cohort_day='2026-08-01' AND day_offset=0`).Scan(&d0); err != nil {
		t.Fatalf("offset 0: %v", err)
	}
	if err := db.DB().QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project='p' AND surface='app'
		   AND cohort_day='2026-08-01' AND day_offset=7`).Scan(&d7); err != nil {
		t.Fatalf("offset 7: %v", err)
	}
	if d0 != 2 || d7 != 1 {
		t.Errorf("cohort = d0 %d d7 %d; want 2 1", d0, d7)
	}
}

func TestAggregateRetentionDayIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewOn("1", "a", cohort)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 1)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var n, actors int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(actors),0) FROM agg_retention WHERE project='p'`).
		Scan(&n, &actors); err != nil {
		t.Fatal(err)
	}
	if n != 1 || actors != 1 {
		t.Errorf("rows=%d actors=%d after replay; want 1 and 1", n, actors)
	}
}

func TestPruneActorsEvictsStale(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	old := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewOn("1", "a", old)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2025, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneActors(ctx, "p", onDay(2026, 8, 23)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var n int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("actors rows = %d after prune, want 0", n)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run 'Actors|Retention' -v`
Expected: FAIL — `db.UpsertActors` undefined.

- [ ] **Step 3: Implement**

Create `internal/store/sqlite/retention.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// Surfaces recorded on actors. A web visitor ID and an app install_id are
// different actors even for the same human, and their retention curves
// describe different populations, so they never share a cohort.
const (
	surfaceWeb = "web"
	surfaceApp = "app"
)

// UpsertActors records first/last seen for every actor active on the given
// day, across all three raw tables. Called before AggregateRetentionDay for
// the same day and before that day's raw rows are deleted.
func (d *DB) UpsertActors(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, src := range []struct{ table, surface string }{
			{"app_views", surfaceApp},
			{"web_hits", surfaceWeb},
			{"product_events", surfaceWeb},
		} {
			q := fmt.Sprintf(`
INSERT INTO actors (project, actor_id, surface, first_seen_day, last_seen_day)
SELECT ?, actor_id, ?, ?, ?
FROM %s WHERE project=? AND substr(ts,1,10)=? AND actor_id <> ''
GROUP BY actor_id
ON CONFLICT(project, actor_id) DO UPDATE SET
  first_seen_day = MIN(actors.first_seen_day, excluded.first_seen_day),
  last_seen_day  = MAX(actors.last_seen_day,  excluded.last_seen_day)`, src.table)
			if _, err := tx.ExecContext(ctx, q,
				project, src.surface, day.String(), day.String(),
				project, day.String()); err != nil {
				return fmt.Errorf("upsert actors from %s: %w", src.table, err)
			}
		}
		return nil
	})
}

// AggregateRetentionDay computes every (cohort, offset) pair that day D
// owns. Each pair is produced by exactly one day — D = cohort + offset — so
// INSERT OR REPLACE is a full recompute of precisely those rows and
// re-running a day is safe. Callers skip anonymous projects: actor_id
// rotates at midnight there, so every cohort would hold only offset 0.
func (d *DB) AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO agg_retention (project, surface, cohort_day, day_offset, actors)
WITH active AS (
  SELECT DISTINCT actor_id FROM app_views     WHERE project=? AND substr(ts,1,10)=?
  UNION
  SELECT DISTINCT actor_id FROM web_hits      WHERE project=? AND substr(ts,1,10)=?
  UNION
  SELECT DISTINCT actor_id FROM product_events WHERE project=? AND substr(ts,1,10)=?
)
SELECT a.project, a.surface, a.first_seen_day,
       CAST(julianday(?) - julianday(a.first_seen_day) AS INTEGER),
       COUNT(DISTINCT a.actor_id)
FROM actors a JOIN active ON active.actor_id = a.actor_id
WHERE a.project=?
GROUP BY a.project, a.surface, a.first_seen_day`,
			project, day.String(), project, day.String(), project, day.String(),
			day.String(), project); err != nil {
			return fmt.Errorf("agg_retention: %w", err)
		}
		return nil
	})
}

// PruneActors evicts actors last seen outside the aggregate window, and the
// cohort rows whose cohort day has aged out. This is what keeps actors
// bounded by yearly-active count rather than all-time.
func (d *DB) PruneActors(ctx context.Context, project string, before civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM actors WHERE project=? AND last_seen_day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune actors: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agg_retention WHERE project=? AND cohort_day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune agg_retention: %w", err)
		}
		return nil
	})
}
```

Add to the `Store` interface:

```go
	UpsertActors(ctx context.Context, project string, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error
	PruneActors(ctx context.Context, project string, before civil.Date) error
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/sqlite/ -run 'Actors|Retention' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): retention cohorts with bounded actors table"
```

---

### Task 6: Identity aggregates

**Files:**
- Create: `internal/store/sqlite/identities.go`
- Test: `internal/store/sqlite/identities_test.go`
- Modify: `internal/store/store.go` (interface)

**Interfaces:**
- Consumes: Tasks 3–4
- Produces:
  - `Store.AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error`
  - `Store.PruneIdentities(ctx context.Context, project string, before civil.Date) error`

- [ ] **Step 1: Write the failing test**

Create `internal/store/sqlite/identities_test.go`:

```go
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

func TestAggregateIdentityDayCountsUsersAndGroups(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	d := civil.Date{Year: 2026, Month: 8, Day: 23}

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "a",
			UserID: "u1", GroupID: "org9", Screen: "/x"},
		{ID: "2", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "b",
			UserID: "u2", GroupID: "org9", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "3", Project: "p", EventName: "subscribed", TS: ts, ReceivedAt: ts,
			ActorID: "a", UserID: "u1", GroupID: "org9"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.AggregateIdentityDay(ctx, "p", d); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	var actors, users, views, events int
	if err := db.DB().QueryRowContext(ctx,
		`SELECT actors, users, views, events FROM agg_identity_daily
		 WHERE project='p' AND day=? AND kind='group' AND id='org9'`, d.String()).
		Scan(&actors, &users, &views, &events); err != nil {
		t.Fatalf("read group row: %v", err)
	}
	if actors != 2 || users != 2 || views != 2 || events != 1 {
		t.Errorf("group row = actors %d users %d views %d events %d; want 2 2 2 1",
			actors, users, views, events)
	}

	if err := db.DB().QueryRowContext(ctx,
		`SELECT actors, views, events FROM agg_identity_daily
		 WHERE project='p' AND day=? AND kind='user' AND id='u1'`, d.String()).
		Scan(&actors, &views, &events); err != nil {
		t.Fatalf("read user row: %v", err)
	}
	if actors != 1 || views != 1 || events != 1 {
		t.Errorf("user row = actors %d views %d events %d; want 1 1 1", actors, views, events)
	}
}

func TestAggregateIdentityDayUpdatesLastSeen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	d := civil.Date{Year: 2026, Month: 8, Day: 23}

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "u1", Name: "Ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "a", UserID: "u1", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateIdentityDay(ctx, "p", d); err != nil {
		t.Fatal(err)
	}

	var last string
	if err := db.DB().QueryRowContext(ctx,
		`SELECT last_seen_day FROM identities WHERE project='p' AND kind='user' AND id='u1'`).
		Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != d.String() {
		t.Errorf("last_seen_day = %q, want %q", last, d.String())
	}
}

func TestPruneIdentitiesEvictsStale(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "old", Name: "Gone"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx,
		`UPDATE identities SET last_seen_day='2024-01-01'`); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneIdentities(ctx, "p", civil.Date{Year: 2026, Month: 8, Day: 23}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("identities rows = %d, want 0", n)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run Identity -v`
Expected: FAIL — `db.AggregateIdentityDay` undefined.

- [ ] **Step 3: Implement**

Create `internal/store/sqlite/identities.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// AggregateIdentityDay rolls one day's activity per user and per group,
// and refreshes identities.last_seen_day so name rows age out with their
// subjects. users is meaningful for kind='group' (distinct users active in
// that group that day) and is always 1 for kind='user'.
func (d *DB) AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, kind := range []string{"user", "group"} {
			col := "user_id"
			if kind == "group" {
				col = "group_id"
			}
			q := fmt.Sprintf(`
INSERT OR REPLACE INTO agg_identity_daily
	(project, day, kind, id, actors, users, hits, views, events)
WITH src AS (
  SELECT %[1]s AS id, actor_id, user_id, 1 AS is_hit,  0 AS is_view, 0 AS is_event
  FROM web_hits       WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 1, 0
  FROM app_views      WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
  UNION ALL
  SELECT %[1]s, actor_id, user_id, 0, 0, 1
  FROM product_events WHERE project=? AND substr(ts,1,10)=? AND %[1]s <> ''
),
ranked AS (
  SELECT id,
         COUNT(DISTINCT actor_id) AS actors,
         COUNT(DISTINCT NULLIF(user_id,'')) AS users,
         SUM(is_hit) AS hits, SUM(is_view) AS views, SUM(is_event) AS events,
         ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, id) AS rn
  FROM src GROUP BY id
)
SELECT ?, ?, ?, id, actors,
       CASE WHEN ? = 'user' THEN 1 ELSE users END,
       hits, views, events
FROM ranked WHERE rn <= %[2]d`, col, topNDimension)

			if _, err := tx.ExecContext(ctx, q,
				project, day.String(), project, day.String(), project, day.String(),
				project, day.String(), kind, kind); err != nil {
				return fmt.Errorf("agg_identity_daily %s: %w", kind, err)
			}

			if _, err := tx.ExecContext(ctx, `
UPDATE identities SET last_seen_day=?
WHERE project=? AND kind=? AND id IN (
  SELECT id FROM agg_identity_daily WHERE project=? AND day=? AND kind=?
)`, day.String(), project, kind, project, day.String(), kind); err != nil {
				return fmt.Errorf("identities last_seen %s: %w", kind, err)
			}
		}
		return nil
	})
}

// PruneIdentities drops name rows and identity aggregates that have aged
// out, on the same window as actors.
func (d *DB) PruneIdentities(ctx context.Context, project string, before civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identities WHERE project=? AND last_seen_day <> '' AND last_seen_day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune identities: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agg_identity_daily WHERE project=? AND day < ?`,
			project, before.String()); err != nil {
			return fmt.Errorf("prune agg_identity_daily: %w", err)
		}
		return nil
	})
}
```

Add to the `Store` interface:

```go
	AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error
	PruneIdentities(ctx context.Context, project string, before civil.Date) error
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/sqlite/ -run Identity -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): user and group identity aggregates"
```

---

### Task 7: Prune extension for app aggregates

**Files:**
- Modify: `internal/store/sqlite/prune.go`
- Modify: `internal/store/store.go` (interface signature)
- Test: `internal/store/sqlite/prune_test.go`

**Interfaces:**
- Consumes: Tasks 4–6
- Produces: `Store.PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error` — signature gains `appBefore`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite/prune_test.go`:

```go
func TestPruneAggregatesRemovesAppTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	old := "2025-01-01"

	for _, stmt := range []string{
		`INSERT INTO agg_app_daily VALUES ('p','` + old + `',1,1,1,0)`,
		`INSERT INTO agg_app_screens VALUES ('p','` + old + `','/x',1,1)`,
		`INSERT INTO agg_app_versions VALUES ('p','` + old + `','ios','1.0',1,1)`,
		`INSERT INTO agg_app_os VALUES ('p','` + old + `','ios','17',1,1)`,
		`INSERT INTO agg_app_devices VALUES ('p','` + old + `','iPhone',1,1)`,
		`INSERT INTO agg_app_countries VALUES ('p','` + old + `','DE',1,1)`,
	} {
		if _, err := db.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	cutoff := civil.Date{Year: 2026, Month: 8, Day: 23}
	if err := db.PruneAggregates(ctx, "p", cutoff, cutoff, cutoff); err != nil {
		t.Fatalf("prune: %v", err)
	}

	for _, table := range []string{
		"agg_app_daily", "agg_app_screens", "agg_app_versions",
		"agg_app_os", "agg_app_devices", "agg_app_countries",
	} {
		var n int
		if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows after prune", table, n)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/sqlite/ -run PruneAggregatesRemovesApp -v`
Expected: FAIL — not enough arguments in call to `db.PruneAggregates`.

- [ ] **Step 3: Implement**

In `internal/store/sqlite/prune.go`, change the signature and add the app tables to the delete list:

```go
func (d *DB) PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error {
```

Add an app group alongside the existing web and product groups:

```go
	appTables := []string{
		"agg_app_daily", "agg_app_screens", "agg_app_versions",
		"agg_app_os", "agg_app_devices", "agg_app_countries",
	}
	for _, t := range appTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+t+` WHERE project=? AND day < ?`,
			project, appBefore.String()); err != nil {
			return fmt.Errorf("prune %s: %w", t, err)
		}
	}
```

Update the interface in `internal/store/store.go`:

```go
	PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/... -v`
Expected: PASS. Any existing caller of `PruneAggregates` in tests needs the third date argument.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): prune app aggregates"
```

---

### Task 8: Pipeline — third item type

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Test: `internal/pipeline/pipeline_test.go`

**Interfaces:**
- Consumes: Task 3 `store.AppView`
- Produces:
  - `pipeline.Sink` gains `WriteAppViews(ctx context.Context, views []store.AppView) error`
  - `(*Buffer).EnqueueAppView(v store.AppView)`

- [ ] **Step 1: Write the failing test**

Append to `internal/pipeline/pipeline_test.go`:

```go
func TestFlushesAppViews(t *testing.T) {
	sink := newFakeSink()
	b := pipeline.New(config.BufferConfig{
		FlushMaxEvents: 2, FlushInterval: time.Hour, Capacity: 10,
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueAppView(store.AppView{ID: "1", Project: "p", Screen: "/a"})
	b.EnqueueAppView(store.AppView{ID: "2", Project: "p", Screen: "/b"})

	waitFor(t, func() bool { return sink.appViewCount() == 2 })
	cancel()
	<-done
}

func TestShutdownDrainsAppViews(t *testing.T) {
	sink := newFakeSink()
	b := pipeline.New(config.BufferConfig{
		FlushMaxEvents: 1000, FlushInterval: time.Hour, Capacity: 10,
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	b.EnqueueAppView(store.AppView{ID: "1", Project: "p", Screen: "/a"})
	cancel()
	<-done

	if got := sink.appViewCount(); got != 1 {
		t.Errorf("app views written on shutdown = %d, want 1", got)
	}
}
```

Extend the existing `fakeSink` in that file with:

```go
func (f *fakeSink) WriteAppViews(_ context.Context, vs []store.AppView) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.appViews = append(f.appViews, vs...)
	return nil
}

func (f *fakeSink) appViewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.appViews)
}
```

adding an `appViews []store.AppView` field to the struct. If a `waitFor` helper does not exist, add:

```go
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/pipeline/ -run AppView -v`
Expected: FAIL — `b.EnqueueAppView` undefined.

- [ ] **Step 3: Implement**

In `internal/pipeline/pipeline.go`:

```go
type Sink interface {
	WriteWebHits(ctx context.Context, hits []store.WebHit) error
	WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error
	WriteAppViews(ctx context.Context, views []store.AppView) error
}

// item carries exactly one of hit/event/view.
type item struct {
	hit   *store.WebHit
	event *store.ProductEvent
	view  *store.AppView
}
```

```go
func (b *Buffer) EnqueueAppView(v store.AppView) { b.enqueue(item{view: &v}) }
```

Replace the two dispatch sites and the flush closure. Both the `ctx.Done()`
drain loop and the normal receive use the same helper, so add it and call it
from both:

```go
func appendItem(it item, hits *[]store.WebHit, events *[]store.ProductEvent, views *[]store.AppView) {
	switch {
	case it.hit != nil:
		*hits = append(*hits, *it.hit)
	case it.event != nil:
		*events = append(*events, *it.event)
	case it.view != nil:
		*views = append(*views, *it.view)
	}
}
```

In `Run`, declare `var views []store.AppView`, extend `flush`:

```go
		if len(views) > 0 {
			b.write(ctx, func(c context.Context) error { return b.sink.WriteAppViews(c, views) }, len(views), "app_views")
			views = nil
		}
```

and change the size trigger to `if len(hits)+len(events)+len(views) >= b.cfg.FlushMaxEvents`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/pipeline/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline
git commit -m "feat(pipeline): buffer and flush app views"
```

---

### Task 9: Ingest envelope — parsing, `$` namespace, attribute merge

**Files:**
- Create: `internal/server/ingest.go`
- Test: `internal/server/ingest_test.go`
- Modify: `internal/identity/identity.go`

**Interfaces:**
- Consumes: Task 1 config constants
- Produces (all in package `server`, lowercase but exercised via an in-package test file):
  - `type envelope struct{ Key string; Attributes map[string]any; Events []rawEvent }`
  - `type rawEvent struct{ ID, TS, Name string; Attributes map[string]any }`
  - `type notice struct{ Index int; Reason string }`
  - `type ingestResult struct{ Accepted, Rejected int; Errors, Warnings []notice }`
  - `type resolved struct{ ... }` with field list below
  - `func mergeAttributes(batch, event map[string]any) map[string]any`
  - `func resolveAttributes(m map[string]any) (resolved, []string)` — returns the resolved struct and unknown `$` keys
  - `func clampTS(client, received time.Time, maxAge time.Duration) (time.Time, bool)`
  - `identity.ActorHash(salt, id, project string) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/server/ingest_test.go` (in-package, so `package server`):

```go
package server

import (
	"testing"
	"time"
)

func TestMergeAttributesEventOverridesBatch(t *testing.T) {
	batch := map[string]any{"$platform": "ios", "$app_version": "2.4.1", "team": "core"}
	event := map[string]any{"$app_version": "2.5.0", "plan": "pro"}

	got := mergeAttributes(batch, event)

	want := map[string]string{
		"$platform": "ios", "$app_version": "2.5.0", "team": "core", "plan": "pro",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("merged has %d keys, want %d", len(got), len(want))
	}
}

func TestMergeAttributesDoesNotMutateBatch(t *testing.T) {
	batch := map[string]any{"$platform": "ios"}
	mergeAttributes(batch, map[string]any{"$platform": "android"})
	if batch["$platform"] != "ios" {
		t.Errorf("batch mutated: $platform = %v", batch["$platform"])
	}
}

func TestResolveAttributesSplitsReservedFromCustom(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{
		"$install_id": "018f", "$user_id": "u1", "$user_name": "Ada",
		"$group_id": "org9", "$group_name": "Acme", "$session_id": "s1",
		"$platform": "ios", "$app_version": "2.4.1", "$os_version": "17.2",
		"$device_model": "iPhone15,2", "$locale": "en-US",
		"$url": "https://x/y", "$referrer": "https://z", "$screen": "/settings",
		"plan": "pro", "count": float64(3),
	})

	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}
	if r.InstallID != "018f" || r.UserID != "u1" || r.UserName != "Ada" {
		t.Errorf("identity = %+v", r)
	}
	if r.GroupID != "org9" || r.GroupName != "Acme" || r.SessionID != "s1" {
		t.Errorf("group/session = %+v", r)
	}
	if r.Platform != "ios" || r.AppVersion != "2.4.1" || r.OSVersion != "17.2" {
		t.Errorf("environment = %+v", r)
	}
	if r.DeviceModel != "iPhone15,2" || r.Locale != "en-US" {
		t.Errorf("device = %+v", r)
	}
	if r.URL != "https://x/y" || r.Referrer != "https://z" || r.Screen != "/settings" {
		t.Errorf("payload = %+v", r)
	}
	if r.Custom["plan"] != "pro" || r.Custom["count"] != "3" {
		t.Errorf("custom = %v", r.Custom)
	}
	if _, ok := r.Custom["$platform"]; ok {
		t.Error("reserved key leaked into custom attributes")
	}
}

func TestResolveAttributesReportsUnknownReservedKeys(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{"$app_ver": "2.4.1", "plan": "pro"})

	if len(unknown) != 1 || unknown[0] != "$app_ver" {
		t.Fatalf("unknown = %v, want [$app_ver]", unknown)
	}
	if _, ok := r.Custom["$app_ver"]; ok {
		t.Error("unknown reserved key must be dropped, not stored")
	}
	if r.Custom["plan"] != "pro" {
		t.Error("ordinary attributes must survive")
	}
}

func TestResolveAttributesTruncatesLongValues(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	r, _ := resolveAttributes(map[string]any{"blob": string(long)})
	if len(r.Custom["blob"]) != maxAttrValue {
		t.Errorf("value length = %d, want %d", len(r.Custom["blob"]), maxAttrValue)
	}
}

func TestClampTS(t *testing.T) {
	received := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * 24 * time.Hour

	cases := []struct {
		name     string
		client   time.Time
		want     time.Time
		clamped  bool
	}{
		{"in range", received.Add(-time.Hour), received.Add(-time.Hour), false},
		{"too old", received.Add(-365 * 24 * time.Hour), received.Add(-maxAge), true},
		{"too far future", received.Add(time.Hour), received, true},
		{"small future skew allowed", received.Add(2 * time.Minute), received.Add(2 * time.Minute), false},
		{"zero falls back to received", time.Time{}, received, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, clamped := clampTS(c.client, received, maxAge)
			if !got.Equal(c.want) || clamped != c.clamped {
				t.Errorf("clampTS = %v, %v; want %v, %v", got, clamped, c.want, c.clamped)
			}
		})
	}
}
```

Also append to `internal/identity/identity_test.go`:

```go
func TestActorHashIsStableAndSalted(t *testing.T) {
	a := identity.ActorHash("salt1", "install-1", "proj")
	b := identity.ActorHash("salt1", "install-1", "proj")
	c := identity.ActorHash("salt2", "install-1", "proj")
	d := identity.ActorHash("salt1", "install-2", "proj")

	if a != b {
		t.Error("same inputs must hash the same")
	}
	if a == c {
		t.Error("rotating the salt must change the hash")
	}
	if a == d {
		t.Error("different installs must not collide")
	}
	if len(a) != 16 {
		t.Errorf("hash length = %d, want 16", len(a))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ ./internal/identity/ -run 'Merge|Resolve|Clamp|ActorHash' -v`
Expected: FAIL — `mergeAttributes` and `identity.ActorHash` undefined.

- [ ] **Step 3: Implement `identity.ActorHash`**

Append to `internal/identity/identity.go`:

```go
// ActorHash salts a client-supplied identifier for anonymous mode. Unlike
// VisitorHash it takes a single identifier: an app's install_id or a
// browser's stored visitor ID carries real entropy, so the result is
// accurate per day where an IP+UA hash collapses behind CGNAT. The salt
// still rotates daily, so cross-day linking remains impossible.
func ActorHash(salt, id, project string) string {
	h := sha256.New()
	for _, part := range []string{salt, id, project} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
```

- [ ] **Step 4: Implement the envelope**

Create `internal/server/ingest.go`:

```go
package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Wire limits (spec §3.7).
const (
	maxBatchEvents = 500
	maxAttrs       = 50
	maxAttrKey     = 64
	maxAttrValue   = 512
	maxNotices     = 10
	futureSkew     = 5 * time.Minute
)

// Reserved event names (spec §3.3).
const (
	namePageview   = "$pageview"
	nameScreenView = "$screen_view"
)

type envelope struct {
	Key        string         `json:"key"`
	Attributes map[string]any `json:"attributes"`
	Events     []rawEvent     `json:"events"`
}

type rawEvent struct {
	ID         string         `json:"id"`
	TS         string         `json:"ts"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

type notice struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type ingestResult struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []notice `json:"errors"`
	Warnings []notice `json:"warnings"`
}

func (res *ingestResult) reject(i int, format string, a ...any) {
	res.Rejected++
	if len(res.Errors) < maxNotices {
		res.Errors = append(res.Errors, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

func (res *ingestResult) warn(i int, format string, a ...any) {
	if len(res.Warnings) < maxNotices {
		res.Warnings = append(res.Warnings, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

// resolved is the reserved half of an event's attributes, split into typed
// fields, plus whatever ordinary attributes remain.
type resolved struct {
	InstallID, UserID, UserName    string
	GroupID, GroupName, SessionID  string
	Platform, AppVersion           string
	OSVersion, DeviceModel, Locale string
	URL, Referrer, Screen          string
	Custom                         map[string]string
}

// reservedKeys maps every system-defined attribute key to its destination.
// The set is closed for *recognition*; unknown $ keys are dropped with a
// warning rather than rejected, so a newer client never loses a batch
// against an older server (spec §3.4).
var reservedKeys = map[string]func(*resolved, string){
	"$install_id":   func(r *resolved, v string) { r.InstallID = v },
	"$user_id":      func(r *resolved, v string) { r.UserID = v },
	"$user_name":    func(r *resolved, v string) { r.UserName = v },
	"$group_id":     func(r *resolved, v string) { r.GroupID = v },
	"$group_name":   func(r *resolved, v string) { r.GroupName = v },
	"$session_id":   func(r *resolved, v string) { r.SessionID = v },
	"$platform":     func(r *resolved, v string) { r.Platform = v },
	"$app_version":  func(r *resolved, v string) { r.AppVersion = v },
	"$os_version":   func(r *resolved, v string) { r.OSVersion = v },
	"$device_model": func(r *resolved, v string) { r.DeviceModel = v },
	"$locale":       func(r *resolved, v string) { r.Locale = v },
	"$url":          func(r *resolved, v string) { r.URL = v },
	"$referrer":     func(r *resolved, v string) { r.Referrer = v },
	"$screen":       func(r *resolved, v string) { r.Screen = v },
}

// mergeAttributes layers per-event attributes over batch defaults, key by
// key. Neither input is mutated: batch defaults are reused across events.
func mergeAttributes(batch, event map[string]any) map[string]any {
	out := make(map[string]any, len(batch)+len(event))
	for k, v := range batch {
		out[k] = v
	}
	for k, v := range event {
		out[k] = v
	}
	return out
}

// resolveAttributes splits merged attributes into typed reserved fields and
// ordinary attributes, returning the unknown $ keys it dropped.
func resolveAttributes(m map[string]any) (resolved, []string) {
	r := resolved{Custom: map[string]string{}}
	var unknown []string
	for k, v := range m {
		if strings.HasPrefix(k, "$") {
			set, ok := reservedKeys[k]
			if !ok {
				unknown = append(unknown, k)
				continue
			}
			set(&r, truncate(stringify(v), maxAttrValue))
			continue
		}
		if len(k) > maxAttrKey || len(r.Custom) >= maxAttrs {
			continue
		}
		r.Custom[k] = truncate(stringify(v), maxAttrValue)
	}
	return r, unknown
}

// stringify renders a JSON scalar the way the attributes blob stores it.
// Numbers decode as float64; integral values print without a decimal point
// so "3" does not become "3.000000".
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clampTS bounds a client timestamp to [received-maxAge, received+skew].
// Out-of-range values are clamped and counted, never dropped: a device with
// a broken clock still contributes. The lower bound is tied to the app raw
// window so a clamped event can never target an aggregated day.
func clampTS(client, received time.Time, maxAge time.Duration) (time.Time, bool) {
	if client.IsZero() {
		return received, false
	}
	if oldest := received.Add(-maxAge); client.Before(oldest) {
		return oldest, true
	}
	if client.After(received.Add(futureSkew)) {
		return received, true
	}
	return client, false
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/server/ ./internal/identity/ -run 'Merge|Resolve|Clamp|ActorHash' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/ingest.go internal/server/ingest_test.go internal/identity
git commit -m "feat(server): ingest envelope, reserved namespace, clock clamping"
```

---

### Task 10: `/api/events` handler

**Files:**
- Modify: `internal/server/handlers.go` — replace `handleHit`/`handleEvent` with `handleEvents`
- Modify: `internal/server/server.go` — routes, `maxBody`, origin rule
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: Tasks 1, 3, 8, 9
- Produces:
  - `server.Enqueuer` gains `EnqueueAppView(v store.AppView)`
  - route `POST /api/events`, `OPTIONS /api/events`; `/api/hit` and `/api/event` removed
  - `maxBody = 256 << 10`

- [ ] **Step 1: Write the failing tests**

Replace the hit/event handler tests in `internal/server/server_test.go` with:

```go
func postEvents(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X) Chrome/120 Safari/537")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEventsRejectsUnknownKey(t *testing.T) {
	h, _ := newTestServer(t)
	rec := postEvents(t, h, `{"key":"nope","events":[{"name":"x"}]}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestEventsAcceptsKeyFromHeader(t *testing.T) {
	h, q := newTestServer(t)
	rec := postEvents(t, h,
		`{"events":[{"name":"subscribed","attributes":{"plan":"pro","$user_id":"u1"}}]}`,
		map[string]string{"X-Analytics-Key": testKey})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(q.events) != 1 || q.events[0].EventName != "subscribed" {
		t.Fatalf("queued = %+v", q.events)
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Errorf("attributes = %v", q.events[0].Attributes)
	}
}

func TestEventsRoutesPageviewAndScreenView(t *testing.T) {
	h, q := newTestServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$platform":"ios","$app_version":"2.4.1"},
	  "events":[
	    {"name":"$pageview","attributes":{"$url":"https://myapp.com/pricing?utm_source=hn"}},
	    {"name":"$screen_view","attributes":{"$screen":"/settings"}},
	    {"name":"subscribed","attributes":{"plan":"pro"}}
	  ]}`
	rec := postEvents(t, h, body, map[string]string{"Origin": testOrigin})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(q.hits) != 1 || q.hits[0].Path != "/pricing" || q.hits[0].UTMSource != "hn" {
		t.Errorf("hits = %+v", q.hits)
	}
	if len(q.views) != 1 || q.views[0].Screen != "/settings" || q.views[0].AppVersion != "2.4.1" {
		t.Errorf("views = %+v", q.views)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestEventsUnknownReservedNameBecomesCustomEventWithWarning(t *testing.T) {
	h, q := newTestServer(t)
	rec := postEvents(t, h, `{"key":"`+testKey+`","events":[{"name":"$pageviews"}]}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	var res ingestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 1 || q.events[0].EventName != "$pageviews" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestEventsUnknownReservedKeyDroppedWithWarning(t *testing.T) {
	h, q := newTestServer(t)
	rec := postEvents(t, h,
		`{"key":"`+testKey+`","events":[{"name":"x","attributes":{"$app_ver":"2","plan":"pro"}}]}`, nil)
	var res ingestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if _, ok := q.events[0].Attributes["$app_ver"]; ok {
		t.Error("unknown reserved key must not be stored")
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Error("ordinary attributes must survive")
	}
}

func TestEventsPerEventRejectionLeavesBatchIntact(t *testing.T) {
	h, q := newTestServer(t)
	body := `{"key":"` + testKey + `","events":[
	  {"name":"good"},
	  {"name":""},
	  {"name":"$pageview"},
	  {"name":"also_good"}]}`
	rec := postEvents(t, h, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	var res ingestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// missing name, and $pageview without $url
	if res.Accepted != 2 || res.Rejected != 2 || len(res.Errors) != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 2 {
		t.Errorf("queued %d events, want 2", len(q.events))
	}
}

func TestEventsRejectsOversizedBatch(t *testing.T) {
	h, _ := newTestServer(t)
	var b strings.Builder
	b.WriteString(`{"key":"` + testKey + `","events":[`)
	for i := 0; i < 501; i++ { // one over the 500-event limit
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"x"}`)
	}
	b.WriteString(`]}`)
	rec := postEvents(t, h, b.String(), nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestEventsOriginPresentMustMatch(t *testing.T) {
	h, _ := newTestServer(t)
	rec := postEvents(t, h, `{"key":"`+testKey+`","events":[{"name":"x"}]}`,
		map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestEventsOriginAbsentIsAccepted(t *testing.T) {
	h, _ := newTestServer(t)
	rec := postEvents(t, h, `{"key":"`+testKey+`","events":[{"name":"x"}]}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}

func TestEventsAnonymousModeHashesIdentifiers(t *testing.T) {
	h, q := newTestServerWithIdentity(t, config.IdentityAnonymous)
	postEvents(t, h,
		`{"key":"`+testKey+`","attributes":{"$user_id":"u1","$group_id":"org9","$user_name":"Ada"},
		  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	v := q.views[0]
	if v.UserID == "u1" || v.UserID == "" {
		t.Errorf("user_id = %q; want a salted hash", v.UserID)
	}
	if v.GroupID != "org9" {
		t.Errorf("group_id = %q; groups stay raw in both modes", v.GroupID)
	}
	if len(q.identities) != 1 || q.identities[0].Kind != store.KindGroup {
		t.Errorf("identities = %+v; $user_name must be ignored in anonymous mode", q.identities)
	}
}

func TestEventsIdentifiedModeStoresRawIdentifiers(t *testing.T) {
	h, q := newTestServerWithIdentity(t, config.IdentityIdentified)
	postEvents(t, h,
		`{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada","$install_id":"i1"},
		  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	v := q.views[0]
	if v.UserID != "u1" || v.ActorID != "u1" {
		t.Errorf("view identity = actor %q user %q; want raw u1", v.ActorID, v.UserID)
	}
	if len(q.identities) != 1 || q.identities[0].Name != "Ada" {
		t.Errorf("identities = %+v", q.identities)
	}
}

func TestEventsBotFilterAppliesOnlyToPageviews(t *testing.T) {
	h, q := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(
		`{"key":"`+testKey+`","events":[
		   {"name":"$pageview","attributes":{"$url":"https://myapp.com/x"}},
		   {"name":"$screen_view","attributes":{"$screen":"/s"}}]}`))
	req.Header.Set("User-Agent", "Googlebot/2.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(q.hits) != 0 {
		t.Errorf("bot pageview should be dropped, got %+v", q.hits)
	}
	if len(q.views) != 1 {
		t.Errorf("bot filter must not touch app views, got %+v", q.views)
	}
}

func TestOldEndpointsAreGone(t *testing.T) {
	h, _ := newTestServer(t)
	for _, path := range []string{"/api/hit", "/api/event"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}
```

Update the test scaffolding in the same file:

```go
const (
	testKey    = "ak_test"
	testOrigin = "https://myapp.com"
)

// ingestResult is unexported, so the external test package decodes into its
// own mirror of the response shape.
type ingestResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Errors   []struct {
		Index  int    `json:"index"`
		Reason string `json:"reason"`
	} `json:"errors"`
	Warnings []struct {
		Index  int    `json:"index"`
		Reason string `json:"reason"`
	} `json:"warnings"`
}

type fakeQueue struct {
	hits       []store.WebHit
	events     []store.ProductEvent
	views      []store.AppView
	identities []store.Identity
}

func (f *fakeQueue) EnqueueHit(h store.WebHit)         { f.hits = append(f.hits, h) }
func (f *fakeQueue) EnqueueEvent(e store.ProductEvent) { f.events = append(f.events, e) }
func (f *fakeQueue) EnqueueAppView(v store.AppView)    { f.views = append(f.views, v) }

type fakeIdentityStore struct{ q *fakeQueue }

func (f fakeIdentityStore) UpsertIdentities(_ context.Context, ids []store.Identity) error {
	f.q.identities = append(f.q.identities, ids...)
	return nil
}

func newTestServer(t *testing.T) (http.Handler, *fakeQueue) {
	return newTestServerWithIdentity(t, config.IdentityAnonymous)
}

func newTestServerWithIdentity(t *testing.T, mode string) (http.Handler, *fakeQueue) {
	t.Helper()
	cfg := loadTestConfig(t, `[{"alias":"myapp","name":"My App","identity":"`+mode+`",
	  "ingest_keys":[{"key":"`+testKey+`","label":"web"}],
	  "allowed_origins":["`+testOrigin+`"]}]`)
	q := &fakeQueue{}
	h := server.New(cfg, q, stubGeo{}, stubSalt{}, fakeIdentityStore{q: q},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h, q
}
```

`loadTestConfig` mirrors `loadWithProjects` from Task 1 but lives in the server test package; `stubGeo` and `stubSalt` already exist — keep them.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -v`
Expected: FAIL — `server.New` takes 5 arguments, `/api/events` not routed.

- [ ] **Step 3: Implement the handler**

Replace `handleHit` and `handleEvent` in `internal/server/handlers.go` with:

```go
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var env envelope
	if !decode(w, r, &env) {
		return
	}

	key := r.Header.Get("X-Analytics-Key")
	if key == "" {
		key = env.Key
	}
	p, label, ok := s.cfg.ProjectByKey(key)
	if !ok {
		// One auth outcome: the key resolves or it does not. No
		// unknown-project oracle to keep indistinguishable.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.Alias) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if len(env.Events) > maxBatchEvents {
		http.Error(w, "too many events", http.StatusRequestEntityTooLarge)
		return
	}

	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	received := time.Now().UTC()
	ip, ua := clientIP(r), r.Header.Get("User-Agent")
	country := s.geo.Country(r, ip)
	botUA := enrich.IsBot(ua)
	maxAge := s.cfg.MaxEventAge()

	var res ingestResponse
	var names []store.Identity
	for i, ev := range env.Events {
		attrs := mergeAttributes(env.Attributes, ev.Attributes)
		rv, unknown := resolveAttributes(attrs)
		for _, k := range unknown {
			res.warn(i, "unknown reserved key %s, ignored", k)
		}

		id, err := eventID(ev.ID)
		if err != nil {
			res.reject(i, "%v", err)
			continue
		}
		ts, clamped := clampTS(parseTS(ev.TS), received, maxAge)
		if clamped {
			res.warn(i, "timestamp out of range, clamped")
		}

		actor, user, group := s.resolveIdentity(p, rv, salt, ip, ua)
		if n := identityNames(p, rv); len(n) > 0 {
			for _, one := range n {
				one.Project = p.Alias
				names = append(names, one)
			}
		}

		switch ev.Name {
		case "":
			res.reject(i, "name is required")

		case namePageview:
			if rv.URL == "" {
				res.reject(i, "$pageview requires $url")
				continue
			}
			page, err := enrich.ParsePageURL(rv.URL)
			if err != nil {
				res.reject(i, "invalid $url")
				continue
			}
			if botUA {
				// Counted as accepted and silently ignored: the client did
				// nothing wrong and must not retry.
				res.Accepted++
				continue
			}
			source := enrich.CleanReferrer(rv.Referrer, page.Host)
			if source == "" && page.Ref != "" {
				source = page.Ref
			}
			device, browser, osName := enrich.ParseUserAgent(ua)
			s.queue.EnqueueHit(store.WebHit{
				ID: id, Project: p.Alias, TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group,
				Path: page.Path, ReferrerSource: source,
				UTMSource: page.UTMSource, UTMMedium: page.UTMMedium,
				UTMCampaign: page.UTMCampaign,
				Country:     country, Device: device, Browser: browser, OS: osName,
			})
			res.Accepted++

		case nameScreenView:
			if rv.Screen == "" {
				res.reject(i, "$screen_view requires $screen")
				continue
			}
			s.queue.EnqueueAppView(store.AppView{
				ID: id, Project: p.Alias, TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group, SessionID: rv.SessionID,
				Screen:  rv.Screen,
				Platform: rv.Platform, AppVersion: rv.AppVersion,
				OSVersion: rv.OSVersion, DeviceModel: rv.DeviceModel,
				Locale: rv.Locale, Country: country,
			})
			res.Accepted++

		default:
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, Project: p.Alias, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group,
				Platform: rv.Platform, AppVersion: rv.AppVersion,
				Attributes: rv.Custom,
			})
			res.Accepted++
		}
	}

	if len(names) > 0 {
		if err := s.names.UpsertIdentities(r.Context(), dedupeIdentities(names)); err != nil {
			s.logger.Error("identity upsert failed", "error", err)
		}
	}
	s.counters.record(label, res.Accepted, res.Rejected)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(res)
}

// resolveIdentity applies the project's identity mode. anonymous salts and
// rotates whatever identifier the client supplied; identified stores it as
// given. group_id is raw in both modes: it identifies an organization, not
// a natural person, and hashing it would make dashboards unreadable for no
// real privacy gain.
func (s *server) resolveIdentity(p *config.Project, rv resolved, salt, ip, ua string) (actor, user, group string) {
	raw := rv.UserID
	if raw == "" {
		raw = rv.InstallID
	}
	if p.Identity == config.IdentityIdentified {
		actor = raw
		if actor == "" {
			actor = identity.VisitorHash(salt, ip, ua, p.Alias)
		}
		return actor, rv.UserID, rv.GroupID
	}
	if raw == "" {
		actor = identity.VisitorHash(salt, ip, ua, p.Alias)
	} else {
		actor = identity.ActorHash(salt, raw, p.Alias)
	}
	if rv.UserID != "" {
		user = identity.ActorHash(salt, rv.UserID, p.Alias)
	}
	return actor, user, rv.GroupID
}

// identityNames collects display names to upsert. $user_name is ignored in
// anonymous mode: a person's name keyed by a hash that rotates daily would
// both defeat the anonymisation and accumulate a row per user per day.
func identityNames(p *config.Project, rv resolved) []store.Identity {
	var out []store.Identity
	if rv.GroupID != "" && rv.GroupName != "" {
		out = append(out, store.Identity{Kind: store.KindGroup, ID: rv.GroupID, Name: rv.GroupName})
	}
	if p.Identity == config.IdentityIdentified && rv.UserID != "" && rv.UserName != "" {
		out = append(out, store.Identity{Kind: store.KindUser, ID: rv.UserID, Name: rv.UserName})
	}
	return out
}

func dedupeIdentities(in []store.Identity) []store.Identity {
	seen := map[string]bool{}
	out := in[:0]
	for _, i := range in {
		k := i.Kind + "\x00" + i.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, i)
	}
	return out
}

// eventID validates a client-supplied UUID or generates one. A supplied id
// is what makes a replayed batch dedupe server-side.
func eventID(id string) (string, error) {
	if id == "" {
		return newID(), nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("id is not a valid UUID")
	}
	return id, nil
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
```

- [ ] **Step 4: Wire the server**

In `internal/server/server.go`:

```go
const maxBody = 256 << 10

type Enqueuer interface {
	EnqueueHit(h store.WebHit)
	EnqueueEvent(e store.ProductEvent)
	EnqueueAppView(v store.AppView)
}

// NameStore is the slice of store.Store the handler needs for display names.
type NameStore interface {
	UpsertIdentities(ctx context.Context, ids []store.Identity) error
}
```

Add `names NameStore` and `counters *keyCounters` to the `server` struct,
take `names` as a `New` parameter after `salt`, initialise the counters in
the struct literal (`counters: newKeyCounters()`), and replace the routes:

```go
	mux.HandleFunc("POST /api/events", s.handleEvents)
	mux.HandleFunc("OPTIONS /api/events", s.handlePreflight)
```

Update `handlePreflight`'s allowed headers:

```go
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-Analytics-Key")
```

Add the per-label counters, which is what makes a key safe to retire — the
summary line shows an old label falling to zero:

```go
// keyCounters accumulates per-key-label accepted/rejected counts for the
// per-minute summary line. Labels only; never the keys themselves.
type keyCounters struct {
	mu   sync.Mutex
	seen map[string][2]int
}

func newKeyCounters() *keyCounters { return &keyCounters{seen: map[string][2]int{}} }

func (c *keyCounters) record(label string, accepted, rejected int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.seen[label]
	c.seen[label] = [2]int{v[0] + accepted, v[1] + rejected}
}

// Drain returns the accumulated counts and resets them.
func (c *keyCounters) Drain() map[string][2]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.seen
	c.seen = map[string][2]int{}
	return out
}
```

Expose it so `cmd/analytics/serve.go` can log it once a minute:

```go
// Counters exposes ingest counters for the periodic summary log.
func (s *server) Counters() *keyCounters { return s.counters }
```

Since `New` returns `http.Handler`, change it to return a concrete
`*Server` type with an exported `ServeHTTP` (rename the struct to `Server`
and add `func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }`,
storing the mux on the struct). Update `cmd/analytics/serve.go` accordingly.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/server/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server
git commit -m "feat(server): unified /api/events endpoint with key auth"
```

---

### Task 11: Tracking script

**Files:**
- Modify: `internal/server/script.js`
- Test: `internal/server/script_test.go`

**Interfaces:**
- Consumes: Task 10 wire format
- Produces: `data-key`, `data-identity`, `data-user`, `data-group` attributes; `window.analytics = {track, identify, reset}`

- [ ] **Step 1: Write the failing test**

The existing `script_test.go` asserts on the embedded source. Replace its
assertions with:

```go
func TestScriptContract(t *testing.T) {
	src := string(trackingScript)
	for _, want := range []string{
		"data-key", "data-identity", "data-user", "data-group",
		"/api/events", "$pageview", "$user_id", "$group_id",
		"analytics_visitor", "analytics_ignore",
		"identify:", "reset:",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("script.js missing %q", want)
		}
	}
	for _, gone := range []string{"data-project", "/api/hit", `"/api/event"`} {
		if strings.Contains(src, gone) {
			t.Errorf("script.js still references removed %q", gone)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -run Script -v`
Expected: FAIL — missing `data-key`.

- [ ] **Step 3: Rewrite `internal/server/script.js`**

```javascript
/* analytics tracking snippet (spec 2026-08-23 §5.1).
 * <script defer src="https://a.example.com/js/script.js"
 *         data-key="ak_9f3c…" data-identity="anonymous"></script>
 * Optional: data-user="u_123" data-group="org_9" when the page renders
 * already knowing who is looking at it.
 */
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;
  var key = script.getAttribute("data-key");
  if (!key) return;

  var endpoint = new URL(script.src).origin;
  // The tag only authorizes localStorage; the server is the enforcement
  // point and salts whatever arrives for an anonymous project regardless.
  var identified = script.getAttribute("data-identity") === "identified";
  var userId = script.getAttribute("data-user") || null;
  var groupId = script.getAttribute("data-group") || null;

  var VISITOR = "analytics_visitor";
  var USER = "analytics_user";
  var GROUP = "analytics_group";

  function ls(name) {
    try { return localStorage.getItem(name); } catch (e) { return null; }
  }
  function lsSet(name, value) {
    try { if (value === null) localStorage.removeItem(name); else localStorage.setItem(name, value); } catch (e) {}
  }

  function ignored() {
    if (ls("analytics_ignore") === "true") return true;
    if (/^localhost$|^127(\.\d+){3}$|^\[::1\]$/.test(location.hostname)) return true;
    if (location.protocol === "file:") return true;
    if (navigator.webdriver) return true;
    return false;
  }

  function uuid() {
    if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function (c) {
      var r = (Math.random() * 16) | 0;
      return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
    });
  }

  // Identity precedence: site-supplied id, else a stored visitor id, else
  // nothing — in which case the server falls back to its rotating hash.
  function visitorId() {
    if (!identified) return null;
    var v = ls(VISITOR);
    if (!v) { v = uuid(); lsSet(VISITOR, v); }
    return v;
  }

  function context() {
    var a = {};
    var u = userId || (identified ? ls(USER) : null);
    var g = groupId || ls(GROUP);
    if (u) a.$user_id = u;
    if (g) a.$group_id = g;
    var v = visitorId();
    if (v) a.$install_id = v;
    return a;
  }

  function send(events) {
    var body = JSON.stringify({ key: key, attributes: context(), events: events });
    // sendBeacon with a string posts text/plain: a CORS-simple request, no
    // preflight, and it survives page unload.
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint + "/api/events", body)) return;
    fetch(endpoint + "/api/events", { method: "POST", body: body, keepalive: true });
  }

  function emit(name, attributes) {
    send([{ id: uuid(), ts: new Date().toISOString(), name: name, attributes: attributes || {} }]);
  }

  var lastPage = null;
  function page() {
    if (ignored()) return;
    var current = location.pathname + location.search;
    if (current === lastPage) return;
    lastPage = current;
    emit("$pageview", { $url: location.href, $referrer: document.referrer });
  }

  function track(name, attributes) {
    if (ignored() || !name) return;
    emit(String(name), attributes);
  }

  var pushState = history.pushState;
  history.pushState = function () {
    pushState.apply(this, arguments);
    page();
  };
  window.addEventListener("popstate", page);

  window.analytics = {
    track: track,
    // Persisted so every later event — this page and future page loads —
    // carries the identity. Events already sent stay unattributed; there is
    // no retroactive stitching.
    identify: function (id, group) {
      userId = id ? String(id) : null;
      if (group) groupId = String(group);
      if (identified) lsSet(USER, userId);
      lsSet(GROUP, groupId);
    },
    // Required on logout: without it the next person on a shared browser
    // inherits the previous user's identity.
    reset: function () {
      userId = null;
      groupId = null;
      lsSet(USER, null);
      lsSet(GROUP, null);
      lsSet(VISITOR, null);
    }
  };

  page();
})();
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server
git commit -m "feat(script): key auth, identity modes, identify/reset"
```

---

### Task 12: Jobs — app class, actors, retention, identities

**Files:**
- Modify: `internal/jobs/jobs.go`
- Test: `internal/jobs/jobs_test.go`

**Interfaces:**
- Consumes: Tasks 4–7 store methods, Task 1 `config.Project.Identity`
- Produces: `RunDailyPass` covering all three classes plus cohorts and identities

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/jobs_test.go`:

```go
func TestDailyPassAggregatesAppDays(t *testing.T) {
	st := newFakeStore()
	st.appDays = []civil.Date{{Year: 2026, Month: 7, Day: 1}}
	r := newRunner(t, st, `[{"alias":"p","name":"P","identity":"identified",
	  "ingest_keys":[{"key":"k","label":"w"}]}]`)

	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(st.aggregatedApp) != 1 {
		t.Errorf("aggregatedApp = %v, want one day", st.aggregatedApp)
	}
	if len(st.upsertedActors) == 0 {
		t.Error("actors were not upserted")
	}
	if len(st.aggregatedRetention) != 1 {
		t.Errorf("aggregatedRetention = %v, want one day", st.aggregatedRetention)
	}
	if len(st.aggregatedIdentity) == 0 {
		t.Error("identity aggregates were not computed")
	}
}

func TestDailyPassSkipsRetentionForAnonymousProjects(t *testing.T) {
	st := newFakeStore()
	st.appDays = []civil.Date{{Year: 2026, Month: 7, Day: 1}}
	r := newRunner(t, st, `[{"alias":"p","name":"P","identity":"anonymous",
	  "ingest_keys":[{"key":"k","label":"w"}]}]`)

	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.aggregatedRetention) != 0 {
		t.Errorf("retention computed for anonymous project: %v", st.aggregatedRetention)
	}
	if len(st.upsertedActors) != 0 {
		t.Errorf("actors upserted for anonymous project: %v", st.upsertedActors)
	}
}

func TestDailyPassPrunesActorsAndIdentities(t *testing.T) {
	st := newFakeStore()
	r := newRunner(t, st, `[{"alias":"p","name":"P","identity":"identified",
	  "ingest_keys":[{"key":"k","label":"w"}]}]`)

	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !st.prunedActors || !st.prunedIdentities {
		t.Errorf("prunedActors=%v prunedIdentities=%v; want both", st.prunedActors, st.prunedIdentities)
	}
}
```

Extend the existing `fakeStore` with the new methods and recording fields:

```go
	appDays             []civil.Date
	aggregatedApp       []civil.Date
	upsertedActors      []civil.Date
	aggregatedRetention []civil.Date
	aggregatedIdentity  []civil.Date
	prunedActors        bool
	prunedIdentities    bool
```

```go
func (f *fakeStore) WriteAppViews(context.Context, []store.AppView) error { return nil }
func (f *fakeStore) UpsertIdentities(context.Context, []store.Identity) error { return nil }

func (f *fakeStore) AppDaysBefore(_ context.Context, _ string, _ civil.Date) ([]civil.Date, error) {
	return f.appDays, nil
}
func (f *fakeStore) AggregateAppDay(_ context.Context, _ string, d civil.Date) error {
	f.aggregatedApp = append(f.aggregatedApp, d)
	return nil
}
func (f *fakeStore) UpsertActors(_ context.Context, _ string, d civil.Date) error {
	f.upsertedActors = append(f.upsertedActors, d)
	return nil
}
func (f *fakeStore) AggregateRetentionDay(_ context.Context, _ string, d civil.Date) error {
	f.aggregatedRetention = append(f.aggregatedRetention, d)
	return nil
}
func (f *fakeStore) AggregateIdentityDay(_ context.Context, _ string, d civil.Date) error {
	f.aggregatedIdentity = append(f.aggregatedIdentity, d)
	return nil
}
func (f *fakeStore) PruneActors(context.Context, string, civil.Date) error {
	f.prunedActors = true
	return nil
}
func (f *fakeStore) PruneIdentities(context.Context, string, civil.Date) error {
	f.prunedIdentities = true
	return nil
}
```

and update the existing `PruneAggregates` fake to the three-date signature.

`newRunner(t, store, projectsJSON)` is a helper this file must define if it
does not already exist: it writes `projectsJSON` to a temp file, loads a
config from it exactly as Task 1's `loadWithProjectsErr` does, and returns
`jobs.New(store, cfg, stubRotator{}, discardLogger, fixedNow)`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jobs/ -v`
Expected: FAIL — `*fakeStore` does not implement `store.Store`, `st.aggregatedApp` unused.

- [ ] **Step 3: Implement**

In `internal/jobs/jobs.go`, inside the per-project loop of `RunDailyPass`,
after the product aggregation block and before `PruneAggregates`:

```go
		appDays, err := r.store.AppDaysBefore(ctx, id, today.AddDays(-ret.App.RawDays))
		if err != nil {
			return err
		}

		// Cohorts need actors recorded before the day's raw rows are
		// deleted, and are undefined for anonymous projects: actor_id
		// rotates at midnight there, so every cohort would hold only
		// offset 0.
		identified := false
		if p := r.cfg.Project(id); p != nil && p.Identity == config.IdentityIdentified {
			identified = true
		}
		for _, day := range appDays {
			if identified {
				if err := r.store.UpsertActors(ctx, id, day); err != nil {
					r.logger.Error("upsert actors failed", "project", id, "day", day.String(), "error", err)
				}
				if err := r.store.AggregateRetentionDay(ctx, id, day); err != nil {
					r.logger.Error("aggregate retention failed", "project", id, "day", day.String(), "error", err)
				}
			}
			if err := r.store.AggregateIdentityDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate identity failed", "project", id, "day", day.String(), "error", err)
			}
			if err := r.store.AggregateAppDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate app failed", "project", id, "day", day.String(), "error", err)
			}
		}
```

Order matters and is load-bearing: `UpsertActors` and `AggregateRetentionDay` and `AggregateIdentityDay` all read the raw rows that `AggregateAppDay` deletes, so app aggregation runs last.

Update the prune call and add the two new prunes:

```go
		if err := r.store.PruneAggregates(ctx, id,
			today.AddDays(-ret.Web.AggregateDays),
			today.AddDays(-ret.Product.AggregateDays),
			today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
		if err := r.store.PruneActors(ctx, id, today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune actors failed", "project", id, "error", err)
		}
		if err := r.store.PruneIdentities(ctx, id, today.AddDays(-ret.App.AggregateDays)); err != nil {
			r.logger.Error("prune identities failed", "project", id, "error", err)
		}
```

Add `"github.com/dmitry/analytics/internal/config"` to the imports if the file does not already have it.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jobs/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jobs
git commit -m "feat(jobs): aggregate app days, cohorts and identities"
```

---

### Task 13: `analytics keygen` and the per-minute summary

**Files:**
- Create: `cmd/analytics/keygen.go`
- Modify: `cmd/analytics/main.go`, `cmd/analytics/serve.go`
- Test: `cmd/analytics/commands_test.go`

**Interfaces:**
- Consumes: Task 10 `(*Server).Counters()`
- Produces: `analytics keygen [-n N]` subcommand; per-minute `ingest summary` log line

- [ ] **Step 1: Write the failing test**

Append to `cmd/analytics/commands_test.go`:

```go
func TestKeygenPrintsUsableKeys(t *testing.T) {
	var out bytes.Buffer
	if err := runKeygen(&out, 2); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "ak_") {
		t.Errorf("output has no ak_ prefixed key:\n%s", s)
	}
	if !strings.Contains(s, "data-key") {
		t.Errorf("output should include a ready-to-paste snippet:\n%s", s)
	}
	if strings.Count(s, "ak_") < 2 {
		t.Errorf("asked for 2 keys, got:\n%s", s)
	}
}

func TestKeygenKeysAreUnique(t *testing.T) {
	var out bytes.Buffer
	if err := runKeygen(&out, 8); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range strings.Fields(out.String()) {
		f = strings.Trim(f, `",`)
		if strings.HasPrefix(f, "ak_") {
			if seen[f] {
				t.Fatalf("duplicate key %q", f)
			}
			seen[f] = true
		}
	}
	if len(seen) < 8 {
		t.Errorf("got %d distinct keys, want 8", len(seen))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/analytics/ -run Keygen -v`
Expected: FAIL — `runKeygen` undefined.

- [ ] **Step 3: Implement**

Create `cmd/analytics/keygen.go`:

```go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// runKeygen prints ingest keys plus a ready-to-paste snippet. Keys are
// public by design — they ship inside app binaries and page source — so
// their job is revocation and project identification, not secrecy. 128 bits
// of entropy makes guessing infeasible regardless.
func runKeygen(out io.Writer, n int) error {
	if n < 1 {
		n = 1
	}
	keys := make([]string, n)
	for i := range keys {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("keygen: entropy: %w", err)
		}
		keys[i] = "ak_" + hex.EncodeToString(buf)
	}

	fmt.Fprintln(out, "Add to projects.json:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, `  "ingest_keys": [`)
	for i, k := range keys {
		comma := ","
		if i == len(keys)-1 {
			comma = ""
		}
		fmt.Fprintf(out, "    { \"key\": %q, \"label\": \"client-%d\" }%s\n", k, i+1, comma)
	}
	fmt.Fprintln(out, "  ]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Web snippet:")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  <script defer src=\"https://analytics.example.com/js/script.js\"\n")
	fmt.Fprintf(out, "          data-key=%q\n", keys[0])
	fmt.Fprintf(out, "          data-identity=\"anonymous\"></script>\n")
	return nil
}
```

In `cmd/analytics/main.go`, add the subcommand to the dispatch switch:

```go
	case "keygen":
		n := 1
		fs := flag.NewFlagSet("keygen", flag.ExitOnError)
		fs.IntVar(&n, "n", 1, "number of keys to generate")
		fs.Parse(os.Args[2:])
		return runKeygen(os.Stdout, n)
```

and list it in the usage text alongside `serve`, `sync`, `migrate`, `version`.

In `cmd/analytics/serve.go`, after the server is constructed, start the
summary goroutine and warn about retired projects:

```go
	for _, alias := range cfg.DisabledKeyProjects() {
		logger.Warn("project has no active ingest keys and can receive nothing", "project", alias)
	}

	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for label, c := range srv.Counters().Drain() {
					logger.Info("ingest summary", "key_label", label,
						"accepted", c[0], "rejected", c[1])
				}
			}
		}
	}()
```

Update the `server.New(...)` call to pass the store as the `names` argument.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/... -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 5: Run the whole gate**

Run: `make check`
Expected: PASS at every floor. If `internal/server` or `internal/config` dipped below 85%, add table-driven cases for the uncovered branches before continuing.

- [ ] **Step 6: Commit**

```bash
git add cmd/analytics
git commit -m "feat(cmd): keygen subcommand and per-label ingest summary"
```

---

### Task 14: Evidence dashboards

**Files:**
- Create: `backoffice/evidence/pages/app/index.md`
- Create: `backoffice/evidence/pages/users/index.md`
- Create: `backoffice/evidence/pages/groups/index.md`
- Create: `backoffice/evidence/pages/retention/index.md`
- Create: `backoffice/evidence/queries/app_*.sql`, `identity_*.sql`, `retention_*.sql`
- Modify: `backoffice/evidence/pages/index.md` — links to the new pages

**Interfaces:**
- Consumes: Task 2 views `v_app_*`, `v_identity_daily`, `v_retention`
- Produces: four dashboard pages

- [ ] **Step 1: Write the queries**

Follow the existing `backoffice/evidence/queries/` convention exactly (one
`.sql` file per query, referenced by filename). Create:

`app_daily.sql`
```sql
select day, actives, views, sessions,
       case when sessions > 0 then duration_sec / sessions else 0 end as avg_duration_sec
from v_app_daily
where project = '${inputs.project.value}'
order by day
```

`app_versions.sql`
```sql
select day, platform || ' ' || app_version as version, actives, views
from v_app_versions
where project = '${inputs.project.value}'
order by day, version
```

`app_screens.sql`
```sql
select screen, sum(views) as views, sum(actives) as actives
from v_app_screens
where project = '${inputs.project.value}'
group by screen order by views desc limit 50
```

`app_platforms.sql`
```sql
select platform, sum(actives) as actives, sum(views) as views
from v_app_versions
where project = '${inputs.project.value}'
group by platform order by actives desc
```

`app_os.sql`
```sql
select platform || ' ' || os_version as os, sum(actives) as actives
from v_app_os
where project = '${inputs.project.value}'
group by os order by actives desc limit 25
```

`app_devices.sql`
```sql
select device_model, sum(actives) as actives
from v_app_devices
where project = '${inputs.project.value}'
group by device_model order by actives desc limit 25
```

`app_countries.sql`
```sql
select country, sum(actives) as actives
from v_app_countries
where project = '${inputs.project.value}'
group by country order by actives desc limit 25
```

`identity_users_daily.sql`
```sql
select day, count(distinct id) as active_users
from v_identity_daily
where project = '${inputs.project.value}' and kind = 'user'
group by day order by day
```

`identity_top_users.sql`
```sql
select d.id as user_id, coalesce(i.name, d.id) as name,
       sum(d.views) + sum(d.events) + sum(d.hits) as activity,
       max(d.day) as last_seen
from v_identity_daily d
left join identities i
  on i.project = d.project and i.kind = 'user' and i.id = d.id
where d.project = '${inputs.project.value}' and d.kind = 'user'
group by d.id, i.name order by activity desc limit 50
```

`identity_groups_daily.sql`
```sql
select day, count(distinct id) as active_groups
from v_identity_daily
where project = '${inputs.project.value}' and kind = 'group'
group by day order by day
```

`identity_top_groups.sql`
```sql
select d.id as group_id, coalesce(i.name, d.id) as name,
       max(d.users) as users,
       sum(d.views) + sum(d.events) + sum(d.hits) as activity,
       max(d.day) as last_seen
from v_identity_daily d
left join identities i
  on i.project = d.project and i.kind = 'group' and i.id = d.id
where d.project = '${inputs.project.value}' and d.kind = 'group'
group by d.id, i.name order by activity desc limit 50
```

`retention_curve.sql`
```sql
select surface, day_offset,
       sum(actors) as actors, sum(cohort_size) as cohort_size,
       case when sum(cohort_size) > 0
            then 100.0 * sum(actors) / sum(cohort_size) else 0 end as retention_pct
from v_retention
where project = '${inputs.project.value}'
group by surface, day_offset order by surface, day_offset
```

`retention_cohorts.sql`
```sql
select surface, cohort_day, day_offset, actors, cohort_size,
       case when cohort_size > 0 then 100.0 * actors / cohort_size else 0 end as retention_pct
from v_retention
where project = '${inputs.project.value}'
order by cohort_day desc, day_offset
```

`projects_identity.sql`
```sql
select alias, name, identity from projects where archived_at is null order by alias
```

- [ ] **Step 2: Write the app page**

`backoffice/evidence/pages/app/index.md` — copy the project-switcher input
block from `pages/web/index.md` verbatim so the pages behave identically,
then:

````markdown
# App

<LineChart data={app_daily} x=day y={['actives','views']} title="Active installs and screen views"/>

<LineChart data={app_daily} x=day y=sessions title="Sessions"/>
<LineChart data={app_daily} x=day y=avg_duration_sec title="Average session length (s)"/>

## Version adoption

<AreaChart data={app_versions} x=day y=actives series=version type=stacked
  title="Active installs by version"/>

## Screens

<DataTable data={app_screens} rows=20/>

## Environment

<BarChart data={app_platforms} x=platform y=actives title="Platform"/>
<DataTable data={app_os} rows=10/>
<DataTable data={app_devices} rows=10/>
<DataTable data={app_countries} rows=10/>
````

The stacked area of `app_versions` is the chart this page exists for: it is
how a rollout is watched and how the version where a metric fell off is
spotted.

- [ ] **Step 3: Write the users and groups pages**

`backoffice/evidence/pages/users/index.md`:

````markdown
# Users

```sql project_identity
select identity from projects where alias = '${inputs.project.value}'
```

{#if project_identity[0].identity === 'identified'}

<LineChart data={identity_users_daily} x=day y=active_users title="Active users"/>

<DataTable data={identity_top_users} rows=25>
  <Column id=name title="User"/>
  <Column id=activity/>
  <Column id=last_seen/>
</DataTable>

{:else}

This project runs in **anonymous** identity mode: `user_id` is a hash that
rotates daily, so per-user reporting is undefined. Set `"identity":
"identified"` in `projects.json` to enable it.

{/if}
````

`backoffice/evidence/pages/groups/index.md` — same shape, but the group
table renders in **both** modes, because `group_id` is stored raw
regardless:

````markdown
# Groups

<LineChart data={identity_groups_daily} x=day y=active_groups title="Active groups"/>

<DataTable data={identity_top_groups} rows=25>
  <Column id=name title="Group"/>
  <Column id=users/>
  <Column id=activity/>
  <Column id=last_seen/>
</DataTable>
````

- [ ] **Step 4: Write the retention page**

`backoffice/evidence/pages/retention/index.md`:

````markdown
# Retention

{#if project_identity[0].identity === 'identified'}

<LineChart data={retention_curve} x=day_offset y=retention_pct series=surface
  title="Retention by day offset (%)"/>

<DataTable data={retention_cohorts} rows=30>
  <Column id=cohort_day/>
  <Column id=day_offset/>
  <Column id=cohort_size/>
  <Column id=actors/>
  <Column id=retention_pct fmt='0.0"%"'/>
</DataTable>

{:else}

Retention is undefined in **anonymous** identity mode: `actor_id` rotates at
midnight, so every cohort would contain only its first day.

{/if}
````

- [ ] **Step 5: Link the pages**

Add the four links to `backoffice/evidence/pages/index.md` next to the
existing Web and Product links.

- [ ] **Step 6: Build the dashboards against a seeded database**

Run the existing evidence build target (check `Makefile` for its exact
name; the base spec's workflow seeds a temp DB then runs `npm run build` in
`backoffice/evidence`).
Expected: all pages prerender without query errors.

- [ ] **Step 7: Commit**

```bash
git add backoffice/evidence
git commit -m "feat(dashboards): app, users, groups and retention pages"
```

---

### Task 15: Documentation and deployment surface

**Files:**
- Create: `docs/ingest-api.md`
- Modify: `README.md`, `docs/deployment.md`, `deploy/install.sh`
- Modify: `scripts/smoke.sh`, `scripts/smokecheck/main.go`

**Interfaces:**
- Consumes: everything above
- Produces: the normative wire-format document that is this project's actual deliverable for client authors

- [ ] **Step 1: Write `docs/ingest-api.md`**

This is a first-class artifact, not a summary: three independently written
clients must behave identically from it alone. It must contain, in this
order:

1. **Endpoint** — `POST /api/events`, and the statement that it is the only
   ingest endpoint.
2. **Authentication** — `X-Analytics-Key` header preferred, `key` in body
   accepted; why browsers must use the body (`sendBeacon` cannot set
   headers); that the key identifies the project so no payload carries one.
3. **Envelope** — the full annotated JSON example from spec §3.1.
4. **Attribute merge** — batch defaults, per-event override, key by key.
5. **Reserved names table** — `$pageview` (requires `$url`), `$screen_view`
   (requires `$screen`), everything else a custom event; unknown `$` names
   stored with a warning.
6. **Reserved keys table** — the full list from Global Constraints; unknown
   `$` keys dropped with a warning.
7. **Identity** — the two modes, the three columns, that `group_id` is
   always raw, that `$user_name` is ignored in anonymous mode.
8. **Timestamps** — RFC 3339 UTC, clamped to
   `[received − max_event_age, received + 5m]`, `max_event_age` equals
   `RETENTION_APP_RAW_DAYS`.
9. **Idempotency** — supply a UUIDv7 `id` to make retries safe; without one
   a replay double-counts.
10. **Limits** — 256 KB body, 500 events, 50 attributes, 64-char keys,
    512-char values.
11. **Responses and retry semantics** — the code table from spec §3.6, with
    the normative sentence: *retry only on 5xx and network failure; any
    other 4xx is a poison batch.* Plus the warning that `202` precedes the
    write and is not a durability receipt.
12. **Batched `$pageview`** — enriched from the connection; a backend must
    not relay pageviews for other people.
13. **A worked offline-queue example** — enqueue with a locally generated
    UUIDv7 and local timestamp, flush up to 500 per request, delete from the
    queue on 2xx/4xx, keep and back off on 5xx.

- [ ] **Step 2: Update `README.md`**

- Replace the tracking snippet with the `data-key` / `data-identity` form.
- Replace every `/api/hit` and `/api/event` reference with `/api/events`.
- Add an "Apps" section pointing at `docs/ingest-api.md`.
- Add `analytics keygen` to the command table.
- Update the privacy section: state plainly that the consent-free posture
  holds for `anonymous` projects and **not** for `identified` ones, because
  a persistent `localStorage` identifier is terminal-equipment storage under
  ePrivacy — the same legal category as a cookie.

- [ ] **Step 3: Update `docs/deployment.md`**

Add an **Upgrading** section listing all six breaking changes from spec §11,
with the exact order of operations:

1. `analytics keygen` and add `ingest_keys` to every project in `projects.json`
2. Deploy the new binary (migrations 003/004 run on boot)
3. Update every site snippet to `data-key` + `data-identity`
4. Note that old snippets return 401 between steps 2 and 3, so schedule
   them together

- [ ] **Step 4: Update `deploy/install.sh`**

Where the script prints follow-up steps, add generating and installing ingest
keys before first start. Keep `shellcheck` clean.

- [ ] **Step 5: Update the smoke tests**

`scripts/smoke.sh` and `scripts/smokecheck/main.go` currently post to
`/api/hit`. Point them at `/api/events` with a key, and add an app-view
assertion so the smoke test covers both routing branches:

```go
	body := `{"key":"` + key + `","attributes":{"$platform":"ios","$app_version":"1.0"},
	  "events":[
	    {"name":"$pageview","attributes":{"$url":"https://example.com/smoke"}},
	    {"name":"$screen_view","attributes":{"$screen":"/smoke"}}]}`
```

The smoke request must send a browser-like `User-Agent`, or the `$pageview`
half is dropped by the bot filter and the check fails for the wrong reason.

- [ ] **Step 6: Full verification**

Run each of these and confirm the output before claiming completion:

```bash
gofmt -l .            # expect: no output
go vet ./...          # expect: no output
make check            # expect: all floors green
make build-all        # expect: three binaries in dist/
./scripts/smoke.sh    # expect: PASS
shellcheck deploy/install.sh scripts/smoke.sh
```

- [ ] **Step 7: Commit**

```bash
git add docs README.md deploy scripts
git commit -m "docs: normative ingest API, upgrade runbook, smoke tests"
```

---

## Verification checklist

Before declaring the plan complete, confirm each spec section has a task:

| Spec section | Task |
|---|---|
| §3.1 single endpoint | 10 |
| §3.2 attribute merge | 9 |
| §3.3 reserved namespace | 9, 10 |
| §3.4 unknown `$` ignored | 9, 10 |
| §3.5 idempotency, clocks | 3, 9 |
| §3.6 response codes, retry | 10, 15 |
| §3.7 limits | 9, 10 |
| §3.8 batched `$pageview` | 10 |
| §3.9 CORS and Origin | 10 |
| §4 authentication | 1, 10, 13 |
| §5 identity model | 1, 9, 10, 11 |
| §5.1 identified web visitors | 11 |
| §5.2 privacy posture | 15 |
| §5.3 late identification | 11 |
| §6.1–6.4 schema and views | 2, 3 |
| §7 retention cohorts | 5, 12 |
| §8 aggregation and jobs | 4, 6, 12 |
| §9 configuration | 1 |
| §10 dashboards | 14 |
| §11 breaking changes | 15 |
| §12 testing | every task |
| §13 repository impact | every task |
