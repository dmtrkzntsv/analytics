# Declared Product Attributes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace discovery-driven product-attribute columns with an operator-declared list that drives both the flat view and the rollups.

**Architecture:** `product_aggregation` (a per-event map plus `enabled` plus `top_n`) collapses to a flat `attributes` JSON array on `projects`, with the cardinality cap moving to a global env setting. Rollups become unconditional, system dimensions roll up automatically as `$`-prefixed keys in the existing `agg_product_attrs`, and `v_events_flat` is rebuilt from config instead of a `DISTINCT ... json_each` scan.

**Tech Stack:** Go (stdlib only in `internal/config`), SQLite via `modernc.org/sqlite`, forward-only SQL migrations embedded with `go:embed`.

**Spec:** `docs/superpowers/specs/2026-08-29-declared-attributes-design.md`

## Global Constraints

- Every task must leave `make check` green (vet + coverage + restore test). Go does not compile half-renamed types, so each task is a vertical slice.
- Migrations are forward-only, numbered `NNN_name.sql`, applied in sorted order inside one transaction each (`internal/store/sqlite/migrate.go`). The next free number is `006`.
- `internal/config` is stdlib-only: `encoding/json`, `fmt`, `io`, `net/url`, `os`, `strings`, `time`. Do not add dependencies to it.
- Env vars are bare names read via `e.str` / `e.num` / `e.dur` in `internal/config/config.go`. The new one is `PRODUCT_ATTRIBUTES_TOP_N`, default `50`.
- The alias charset is exactly `^[a-z0-9]+$`.
- Attribute column names in `v_events_flat` are `attr_` + `sanitizeAlias(key)`; that sanitisation stays, and hostile-key tests in `flatview_test.go` must keep passing.
- Commit messages follow Conventional Commits (`CLAUDE.md`). `feat`, `fix`, `perf` appear in release notes; use them for user-visible behaviour.
- Do not rename `v_events_flat`. It stays a single shared view.
- **Test idioms in `internal/store/sqlite`.** Tests live in `package sqlite`, so they reach unexported state directly. The database helper is `newTestDB(t)` (`openRegistryDB(t)` for registry-only tests); queries use `db.db.QueryRow(...)`; timestamps use the existing `ts("2026-08-01T10:00:00Z")` helper; dates use `civil.DateOf(ts(...))` — there is no `civil.MustParse`. Seeding goes through the real write path (`db.WriteProductEvents`), not raw INSERTs, except where a task explicitly needs to fabricate a pre-migration row. Any other helper a task's tests use (`viewColumns`, `viewSQL`, `readAttrs`, `seedDeclaredProject`, `seedLegacyAliasDirect`, `seedProductEvent`) does **not** exist yet — the task that first uses it defines it.

---

### Task 1: Replace `product_aggregation` with `attributes`

The load-bearing task: the type flows `RegistryProject.Aggregation` -> `manage.Project.Aggregation` -> `store.ProductAggSettings` -> `aggregate_product.go`, so the whole chain changes together or nothing compiles.

**Files:**
- Create: `internal/store/sqlite/migrations/006_attributes.sql`
- Modify: `internal/config/config.go` (drop `ProductAggregation`, add `Attributes []string` to `Project`, add `ProductAttributesTopN` to `Config`)
- Modify: `internal/store/store.go` (`RegistryProject.Aggregation` -> `Attributes`; drop `ProductAggSettings`; change `AggregateProductDay` signature)
- Modify: `internal/store/sqlite/registry.go:36-40,82-112` (three SQL statements)
- Modify: `internal/store/sqlite/aggregate_product.go:20-93`
- Modify: `internal/manage/registry.go:93-100,228-237`
- Modify: `internal/manage/ops.go:25-31,53-80`
- Modify: `internal/manage/importexport.go:208`
- Modify: `internal/jobs/jobs.go:107-113`
- Modify: `internal/mcpserver/tools_manage.go:36-45`, `internal/mcpserver/tools_product.go:61-64`, `internal/mcpserver/guide.go:100-102`
- Test: `internal/store/sqlite/registry_test.go`, `internal/manage/registry_test.go`, `internal/store/sqlite/aggregate_product_test.go`

**Interfaces:**
- Produces: `store.RegistryProject.Attributes string` (JSON array, `"[]"` when none); `store.Store.AggregateProductDay(ctx, project string, day civil.Date, attrs []string, topN int) error`; `manage.ProjectSpec.Attributes []string`; `func (s *Snapshot) AttributesFor(alias string) []string`; `config.Config.ProductAttributesTopN int`.
- Consumes: nothing from earlier tasks.

- [ ] **Step 1: Write the failing migration test**

In `internal/store/sqlite/registry_test.go`:

```go
func TestMigrationBackfillsAttributes(t *testing.T) {
	db := newTestDB(t) // existing helper; applies all migrations
	ctx := context.Background()
	// Simulate a pre-006 row by writing the new column directly is not
	// possible post-migration, so assert the column exists and defaults.
	if _, err := db.ExecForTest(
		`INSERT INTO projects (id, alias, name, identity, allowed_origins)
		 VALUES ('i1','blog','Blog','anonymous','[]')`); err != nil {
		t.Fatal(err)
	}
	ps, _, err := db.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Attributes != "[]" {
		t.Fatalf("Attributes = %q, want \"[]\"", ps[0].Attributes)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestMigrationBackfillsAttributes -v`
Expected: FAIL — `ps[0].Attributes` undefined (field does not exist yet).

- [ ] **Step 3: Write the migration**

`internal/store/sqlite/migrations/006_attributes.sql`:

```sql
-- product_aggregation collapses to a flat declared key list. `enabled` is
-- dropped (rollups are now unconditional) and `top_n` moves to the global
-- PRODUCT_ATTRIBUTES_TOP_N setting. The backfill takes the DISTINCT union
-- of every array in the old event-keyed map, so
--   {"*":["plan"],"subscribed":["tier","plan"]}  ->  ["plan","tier"]
ALTER TABLE projects ADD COLUMN attributes TEXT NOT NULL DEFAULT '[]';

UPDATE projects SET attributes = COALESCE((
  SELECT json_group_array(k) FROM (
    SELECT DISTINCT v.value AS k
    FROM json_each(json_extract(projects.product_aggregation, '$.attributes')) AS m,
         json_each(m.value) AS v
    ORDER BY 1
  )
), '[]')
WHERE product_aggregation IS NOT NULL
  AND product_aggregation <> ''
  AND json_extract(product_aggregation, '$.attributes') IS NOT NULL;

ALTER TABLE projects DROP COLUMN product_aggregation;
```

Verify the correlated `json_each` reference works on this SQLite build; if it does not, replace the `UPDATE` with the equivalent using a `json_tree` walk over `$.attributes` selecting rows where `type='text'`.

- [ ] **Step 4: Change the storage types**

`internal/store/store.go`: rename `RegistryProject.Aggregation` to `Attributes` (same `string` type, JSON array), delete `ProductAggSettings`, and change the interface method to:

```go
AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error
```

`internal/store/sqlite/registry.go`: in `LoadRegistry`, replace `COALESCE(product_aggregation,'')` with `attributes` and scan into `p.Attributes`. In `CreateProject`, replace the `product_aggregation` column and its `NULLIF(?,'')` with `attributes` and a plain `?`. In `UpdateProject`, the same. Attributes is `NOT NULL DEFAULT '[]'`, so never wrap it in `NULLIF`.

- [ ] **Step 5: Drop `enabled` from aggregation**

`internal/store/sqlite/aggregate_product.go`: change `AggregateProductDay` to the new signature, delete the `if agg.Enabled {` guard (`:33-39`) so `rollupProduct` always runs before the `DELETE`, and change `rollupProduct` / the attribute loop to take `attrs []string, topN int`. The per-event map union at `:78-91` collapses to:

```go
for _, event := range events {
	for _, key := range attrs {
		if err := d.rollupAttr(ctx, tx, project, day, from, to, event, key, topN); err != nil {
			return fmt.Errorf("attr %s/%s: %w", event, key, err)
		}
	}
}
```

`rollupAttr` is unchanged — its `json_extract(...) IS NOT NULL` filter already makes a key absent from an event produce zero rows.

- [ ] **Step 6: Update the manage layer**

`internal/manage/registry.go`: `Project.Aggregation *config.ProductAggregation` becomes `Attributes []string`, parsed from the `attributes` JSON array (drop the `TopN == 0` defaulting at `:98-99`). Replace `AggregationFor` with:

```go
// AttributesFor returns the project's declared attribute keys. Unknown
// aliases return nil, matching the archived-project fallback.
func (s *Snapshot) AttributesFor(alias string) []string {
	p := s.byAlias[alias]
	if p == nil {
		return nil
	}
	return p.Attributes
}
```

`internal/manage/ops.go`: `ProjectSpec.Aggregation` becomes `Attributes []string`; in `row()`, marshal it to JSON, defaulting `nil` to `"[]"` exactly as `AllowedOrigins` does.

`internal/manage/importexport.go`: the export struct at `:208` gains `Attributes []string` with tag `json:"attributes"` **and keeps** a `ProductAggregation *config.LegacyAggregation` field tagged `json:"product_aggregation"`. The v1 decoder calls `dec.DisallowUnknownFields()` (`:89`), so dropping the old field would make every previously-exported document fail to import. Resolve the two the same way `config.Project.DeclaredAttributes` does, preferring `attributes` when both are present. Export writes only `attributes`; add `omitempty` to the legacy field so round-tripped documents stop carrying it.

In the legacy bare-array branch (`:80`), `ProductAggregation: lp.ProductAggregation` becomes `Attributes: lp.DeclaredAttributes()`.

- [ ] **Step 7: Update the callers**

`internal/jobs/jobs.go:107-113`: replace `settings := snap.AggregationFor(id)` with `attrs := snap.AttributesFor(id)` and pass `attrs, r.topN` to `AggregateProductDay`. Add a `topN int` field to `Runner`, set from `cfg.ProductAttributesTopN` wherever the runner is constructed.

`internal/config/config.go`: add `ProductAttributesTopN: e.num("PRODUCT_ATTRIBUTES_TOP_N", 50)` to the `Config` literal in `parse`, with the matching struct field.

`config.Project` is the **legacy** `projects.json` format read by `config import`, so it must keep parsing the old block — deleting `ProductAggregation` outright would break the documented upgrade path. Give it both shapes and one accessor:

```go
type Project struct {
	// ... existing fields ...
	Attributes []string `json:"attributes"`
	// LegacyAggregation is the pre-2026-08 product_aggregation block.
	// Import still accepts it and folds its event-keyed map into a flat
	// list, so an unmodified pre-upgrade projects.json still imports.
	LegacyAggregation *LegacyAggregation `json:"product_aggregation"`
}

type LegacyAggregation struct {
	Attributes map[string][]string `json:"attributes"`
}

// DeclaredAttributes returns the declared keys, folding the legacy
// event-keyed map into a sorted DISTINCT union when only it is present.
func (p *Project) DeclaredAttributes() []string {
	if len(p.Attributes) > 0 || p.LegacyAggregation == nil {
		return p.Attributes
	}
	seen := map[string]bool{}
	var out []string
	for _, keys := range p.LegacyAggregation.Attributes {
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
```

`sort` is stdlib, so this respects the package's stdlib-only constraint. The `enabled` and `top_n` fields of the old block are parsed and discarded — that is the intended behaviour change, not a loss.

`internal/mcpserver/tools_manage.go:36-45`: delete `aggregationIn` and its converter; the project input takes `Attributes []string` with jsonschema `"attribute keys to break down and expose as flat-view columns"`.

`internal/mcpserver/tools_product.go:61-64`: delete the `Aggregation == nil || !Enabled` guard entirely — rollups are unconditional now, so the tool no longer refuses.

`internal/mcpserver/guide.go:100-102`: report the declared list instead of an enabled flag.

- [ ] **Step 8: Run the full suite**

Run: `make check`
Expected: PASS. Update any test fixture still using `product_aggregation` JSON or `ProductAggSettings`.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat(config)!: declare product attributes as a flat list

BREAKING CHANGE: product_aggregation is replaced by a flat attributes
array. enabled is gone and rollups always run, so projects that never
opted in stop losing product history at the raw retention boundary.
top_n moves to the global PRODUCT_ATTRIBUTES_TOP_N setting."
```

---

### Task 2: Roll system dimensions up automatically

**Files:**
- Modify: `internal/store/sqlite/aggregate_product.go` (`rollupProduct`)
- Test: `internal/store/sqlite/aggregate_product_test.go`

**Interfaces:**
- Consumes: `AggregateProductDay(ctx, project, day, attrs []string, topN int)` from Task 1.
- Produces: `agg_product_attrs` rows with `attr_key` in `{"$platform","$app_version"}`, written regardless of `attrs`.

- [ ] **Step 1: Write the failing test**

```go
func TestRollupWritesSystemDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
		map[string]string{}, "ios", "1.2.0") // platform, app_version columns
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project='blog' AND attr_key='$platform'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "ios" {
		t.Fatalf("$platform = %q, want ios", v)
	}
}
```

`seedProductEvent` does not exist yet — add it in this task, next to the existing `seedProductDay` helper. It wraps the real write path rather than raw SQL, so the stored shape always matches production:

```go
// seedProductEvent writes one product event. platform and appVersion are
// the typed system columns; attrs is the custom JSON blob.
func seedProductEvent(t *testing.T, db *DB, project, event, at string,
	attrs map[string]string, platform, appVersion string) {
	t.Helper()
	if err := db.WriteProductEvents(context.Background(), []store.ProductEvent{{
		ID: uuid.NewString(), Project: project, EventName: event,
		ActorID: "u1", TS: ts(at), Attributes: attrs,
		Platform: platform, AppVersion: appVersion,
	}}); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestRollupWritesSystemDimensions -v`
Expected: FAIL — `sql: no rows in result set`.

- [ ] **Step 3: Implement the system-dimension rollup**

In `rollupProduct`, after the declared-key loop, add a column-sourced rollup. It mirrors `rollupAttr` but reads a real column instead of a JSON path, so it takes the column name and the `$`-prefixed key it is stored under:

```go
// systemDims maps a product_events column to the attr_key it rolls up
// under. The $ prefix is safe as a namespace because resolveAttributes
// routes every $-prefixed input to a typed field, so a custom key can
// never collide with one of these.
var systemDims = []struct{ column, key string }{
	{"platform", "$platform"},
	{"app_version", "$app_version"},
}
```

For each entry, run the same two statements `rollupAttr` runs (top-N then the `(other)` tail), substituting `json_extract(attributes, :path)` with the bare column and dropping the `IS NOT NULL` filter in favour of `<column> <> ''` — these columns are `NOT NULL DEFAULT ''`, so empty means absent.

Extract the shared statement pair into one helper taking a value expression string so the two paths do not duplicate the ranked-top-N SQL.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/store/sqlite/ -run TestRollup -v`
Expected: PASS, including the existing declared-key rollup tests.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): roll product system dimensions up automatically

platform and app_version are written on every product event but had no
aggregate path, so they vanished at the raw retention boundary."
```

---

### Task 3: Build `v_events_flat` from config

**Files:**
- Modify: `internal/store/sqlite/flatview.go`
- Modify: `internal/store/store.go` (drop `KnownAttributeKeys`)
- Modify: `internal/app/app.go:93-98`, `internal/jobs/jobs.go:139-143`, `internal/manage/ops.go`
- Modify: `internal/manage/registry.go` (add `DeclaredAttributeKeys`)
- Test: `internal/store/sqlite/flatview_test.go`, `internal/manage/registry_test.go`

**Interfaces:**
- Consumes: `Snapshot.AttributesFor` from Task 1.
- Produces: `func (s *Snapshot) DeclaredAttributeKeys() []string` — sorted, deduplicated union across every project including archived ones.

- [ ] **Step 1: Write the failing tests**

```go
func TestFlatViewOnlyDeclaredKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
		map[string]string{"plan": "pro", "undeclared": "x"}, "", "")
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	cols := viewColumns(t, db, "v_events_flat")
	if !cols["attr_plan"] || cols["attr_undeclared"] {
		t.Fatalf("columns = %v, want attr_plan and not attr_undeclared", cols)
	}
	// Undeclared keys stay reachable through the raw JSON base column.
	var got string
	if err := db.db.QueryRow(
		`SELECT json_extract(attributes,'$.undeclared') FROM v_events_flat`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "x" {
		t.Fatalf("undeclared via attributes = %q, want x", got)
	}
}

func TestRebuildFlatViewIsNoOpWhenUnchanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	before := viewSQL(t, db, "v_events_flat")
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if viewSQL(t, db, "v_events_flat") != before {
		t.Fatal("view was rebuilt despite an unchanged column set")
	}
}
```

`viewColumns` reads `pragma_table_info`; `viewSQL` reads `sqlite_master.sql`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/sqlite/ -run TestFlatView -v`
Expected: FAIL — `attributes` is not yet a base column.

- [ ] **Step 3: Add the base column and the no-op check**

In `flatview.go`, add `"attributes"` to `flatViewBaseColumns`. Before the drop/create transaction, compare the desired column list against `pragma_table_info('v_events_flat')` and return `nil` when they match. Delete `KnownAttributeKeys` and remove it from the `store.Store` interface.

- [ ] **Step 4: Add `DeclaredAttributeKeys`**

In `internal/manage/registry.go`:

```go
// DeclaredAttributeKeys is the sorted, deduplicated union of every
// project's declared keys — the column set of v_events_flat. Archived
// projects are included: archiving keeps their data queryable.
func (s *Snapshot) DeclaredAttributeKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range s.ordered {
		for _, k := range p.Attributes {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 5: Rewire the three triggers**

`internal/app/app.go:93-98` and `internal/jobs/jobs.go:139-143` drop the `KnownAttributeKeys` call and pass `snap.DeclaredAttributeKeys()`. In `internal/manage/ops.go`, add an unexported `rebuildFlatView(ctx)` helper called at the end of `CreateProject`, `UpdateProject` and `Import`, after `Reg.Reload`, logging rather than failing the operation on error.

- [ ] **Step 6: Run the suite**

Run: `make check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "perf(store): build v_events_flat from declared config

Drops the DISTINCT json_each scan over product_events on boot and in the
daily pass, and stops a stray client key from widening the view."
```

---

### Task 4: Alias charset and the immutability guard

**Files:**
- Modify: `internal/manage/ops.go:32-53` (`validate`)
- Modify: `cmd/twillingate/project.go:130-133`
- Test: `internal/manage/ops_test.go`, `cmd/twillingate/project_test.go`

**Interfaces:**
- Produces: `validate()` rejecting aliases outside `^[a-z0-9]+$`, applied on create and rename but NOT on update.

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateRejectsBadAlias(t *testing.T) {
	for _, bad := range []string{"My-Blog", "my_app", "shop uk", "blog!", ""} {
		if _, err := ops.CreateProject(ctx, "test",
			ProjectSpec{Alias: bad, Identity: "anonymous"}); err == nil {
			t.Errorf("CreateProject(%q) = nil error, want rejection", bad)
		}
	}
	for _, ok := range []string{"blog", "blog2", "2048"} {
		if _, err := ops.CreateProject(ctx, "test",
			ProjectSpec{Alias: ok, Identity: "anonymous"}); err != nil {
			t.Errorf("CreateProject(%q) = %v, want nil", ok, err)
		}
	}
}

func TestUpdateDoesNotCharsetCheck(t *testing.T) {
	// A legacy row predating the rule must stay editable, or `config
	// export | config import` locks its owner out of the fix.
	seedLegacyAliasDirect(t, st, "my_app")
	if _, err := ops.UpdateProject(ctx, "test",
		ProjectSpec{Alias: "my_app", Name: "renamed", Identity: "anonymous"}); err != nil {
		t.Fatalf("UpdateProject on legacy alias = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/manage/ -run 'TestCreateRejectsBadAlias|TestUpdateDoesNotCharsetCheck' -v`
Expected: FAIL — bad aliases are currently accepted.

- [ ] **Step 3: Implement**

Split `validate()` into `validate()` (shared rules) and `validateNew()` (shared rules plus the charset). `CreateProject` and the rename op call `validateNew`; `UpdateProject` keeps calling `validate`. Charset check, stdlib only:

```go
// validAlias is ^[a-z0-9]+$. The alias is the project column on every
// stored row and the dashboard label, so it is kept to one predictable
// shape; it is never spliced into SQL.
func validAlias(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Improve the CLI not-found message**

`cmd/twillingate/project.go:132` becomes:

```go
fmt.Fprintf(stdout, "no project %q; aliases are immutable — use `project rename` to change one, or `project create` to make a new one\n", *alias)
```

- [ ] **Step 5: Run the tests, then commit**

Run: `make check`

```bash
git add -A
git commit -m "feat(manage): require project aliases to match ^[a-z0-9]+\$"
```

---

### Task 5: `project rename`

**Files:**
- Modify: `internal/store/store.go` (add `RenameProject`)
- Modify: `internal/store/sqlite/registry.go` (implement it next to `DeleteProjectData`)
- Modify: `internal/manage/ops.go` (add `RenameProject`)
- Modify: `cmd/twillingate/project.go` (new `rename` subcommand)
- Test: `internal/store/sqlite/registry_test.go`, `cmd/twillingate/project_test.go`

**Interfaces:**
- Consumes: `validateNew` from Task 4; `projectTables` (`internal/store/sqlite/registry.go:182`).
- Produces: `RenameProject(ctx context.Context, old, new string, a store.AuditEntry) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestRenameProjectMovesEveryTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "blog")
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z", nil, "", "")
	if err := db.RenameProject(ctx, "blog", "journal", store.AuditEntry{
		Actor: "test", Action: "project.rename", Subject: "blog->journal"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"product_events", "ingest_keys"} {
		var n int
		if err := db.db.QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE project='blog'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows under the old alias", table, n)
		}
	}
	var keys int
	if err := db.db.QueryRow(
		`SELECT COUNT(*) FROM ingest_keys WHERE project='journal'`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys == 0 {
		t.Fatal("ingest keys did not follow the rename; deployed clients would break")
	}
}

func TestRenameProjectRejectsExistingTarget(t *testing.T) { /* seed two, expect error */ }
func TestRenameProjectUnknownSource(t *testing.T)        { /* expect error, no writes */ }
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestRenameProject -v`
Expected: FAIL — `db.RenameProject` undefined.

- [ ] **Step 3: Implement the store method**

Beside `DeleteProjectData`, reusing the same `projectTables` list:

```go
// RenameProject rewrites the alias on the registry row and the project
// column on every table keyed by it, in one transaction. ingest_keys is in
// projectTables, so keys follow the rename and deployed clients keep
// working. PRAGMA foreign_keys is never enabled, so the
// REFERENCES projects(alias) clause does not constrain statement order.
func (d *DB) RenameProject(ctx context.Context, old, new string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM projects WHERE alias=?`, new).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("rename: alias %q already exists", new)
		}
		res, err := tx.ExecContext(ctx, `UPDATE projects SET alias=? WHERE alias=?`, new, old)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("rename: unknown alias %q", old)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET project=? WHERE project=?`, new, old); err != nil {
				return fmt.Errorf("rename %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}
```

- [ ] **Step 4: Add the ops wrapper and CLI subcommand**

`Ops.RenameProject(ctx, actor, old, new)` runs `validateNew` on `new`, calls the store with action `project.rename` and subject `old+"->"+new`, then `Reg.Reload` and the flat-view rebuild helper from Task 3.

In `cmd/twillingate/project.go`, add a `rename` case with `-alias` and `-to` flags, both required, printing `project %q renamed to %q`.

- [ ] **Step 5: Run the suite, then commit**

Run: `make check`

```bash
git add -A
git commit -m "feat(cmd): add project rename

Rewrites the project column across every keyed table in one transaction.
Ingest keys follow the rename, so deployed clients keep working."
```

---

### Task 6: CLI and MCP attribute flags

**Files:**
- Modify: `cmd/twillingate/project.go:105-165`
- Modify: `internal/mcpserver/tools_manage.go`
- Test: `cmd/twillingate/project_test.go`

**Interfaces:**
- Consumes: `manage.ProjectSpec.Attributes []string` from Task 1.

- [ ] **Step 1: Write the failing test**

```go
func TestProjectAttrFlag(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-alias", "blog",
		"-attr", "plan", "-attr", "tier"}, &out); code != 0 {
		t.Fatalf("create = %d: %s", code, out.String())
	}
	out.Reset()
	run([]string{"config", "export"}, &out)
	if !strings.Contains(out.String(), `"plan"`) ||
		!strings.Contains(out.String(), `"tier"`) {
		t.Fatalf("export missing declared attributes: %s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/twillingate/ -run TestProjectAttrFlag -v`
Expected: FAIL — `flag provided but not defined: -attr`.

- [ ] **Step 3: Implement**

Add `var attrs multiFlag` and `sf.Var(&attrs, "attr", "attribute key to break down (repeatable)")` next to the existing `-origin` handling. For `create`, set `spec.Attributes = attrs`. For `update`, start from `current.Attributes` and overwrite inside the existing `sf.Visit` switch under `case "attr":`, matching the documented replace-the-whole-list semantics `-origin` already has.

In `internal/mcpserver/tools_manage.go`, the create/update inputs take `Attributes []string` and pass it straight through.

- [ ] **Step 4: Run the tests, then commit**

Run: `make check`

```bash
git add -A
git commit -m "feat(cmd): set product attributes with a repeatable -attr flag

Replaces the config export/edit/import round-trip that was the only way
to declare them."
```

---

### Task 7: `v_product_attrs` stitch view

The most intricate task. `internal/store/sqlite/migrations/002_views.sql:5-8` states the binding invariant: a view's live half must be numerically identical to what aggregation writes, or a dashboard jumps the moment a day ages over.

**Files:**
- Create: `internal/store/sqlite/migrations/007_product_attrs_view.sql`
- Create: `evidence/sources/twillingate/v_product_attrs.sql`
- Delete: `evidence/sources/twillingate/agg_product_attrs.sql`
- Modify: `evidence/pages/product/[project].md:71-83`
- Modify: `internal/mcpserver/tools_product.go:65-67` (query `v_product_attrs`)
- Modify: `internal/app/app.go` (write `PRODUCT_ATTRIBUTES_TOP_N` to `meta` at boot)
- Test: `internal/store/sqlite/views_test.go`

**Interfaces:**
- Consumes: the rollup semantics from Tasks 1 and 2 — the live half must match `rollupAttr` and the system-dimension rollup exactly.

- [ ] **Step 1: Write the failing invariant test**

Follow the existing pattern in `views_test.go`: seed raw product events for one day, read `v_product_attrs`, run `AggregateProductDay`, read it again, and assert the two results are identical.

```go
func TestProductAttrsViewInvariant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedDeclaredProject(t, db, "blog", []string{"plan"})
	// more distinct values than the cap, to exercise (other)
	for i := 0; i < 60; i++ {
		seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
			map[string]string{"plan": fmt.Sprintf("p%02d", i)}, "ios", "1.0")
	}
	before := readAttrs(t, db, "blog", "2026-08-01")
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, "blog", "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("view changed when the day aggregated:\nbefore %v\nafter  %v", before, after)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/sqlite/ -run TestProductAttrsViewInvariant -v`
Expected: FAIL — `no such table: v_product_attrs`.

- [ ] **Step 3: Write the migration**

Both inputs are reachable from SQL, so this is a static view, not a runtime-generated one: declared keys live in `projects.attributes` (expanded with `json_each`) and the cap lives in `meta`. Aggregation deletes the day's raw rows in the same transaction, so raw and aggregated days are disjoint and the live half needs no day exclusion — the same reasoning `002_views.sql:2-3` records for the other stitch views.

`internal/store/sqlite/migrations/007_product_attrs_view.sql`:

```sql
-- Stitch view for attribute breakdowns. The live half must stay
-- numerically identical to rollupAttr and the system-dimension rollup in
-- aggregate_product.go, or a dashboard jumps when a day ages over.
CREATE VIEW v_product_attrs AS
SELECT project, day, event_name, attr_key, attr_value, count, unique_users
FROM agg_product_attrs
UNION ALL
WITH cap AS (
  SELECT CAST(value AS INTEGER) AS n FROM meta WHERE key='product_attributes_top_n'
),
declared AS (
  SELECT p.alias AS project, j.value AS attr_key
  FROM projects p, json_each(p.attributes) j
),
vals AS (
  SELECT pe.project, substr(pe.ts,1,10) AS day, pe.event_name, d.attr_key,
         json_extract(pe.attributes, '$."' || d.attr_key || '"') AS attr_value,
         pe.actor_id
  FROM product_events pe
  JOIN declared d ON d.project = pe.project
  WHERE json_extract(pe.attributes, '$."' || d.attr_key || '"') IS NOT NULL
  UNION ALL
  SELECT project, substr(ts,1,10), event_name, '$platform', platform, actor_id
  FROM product_events WHERE platform <> ''
  UNION ALL
  SELECT project, substr(ts,1,10), event_name, '$app_version', app_version, actor_id
  FROM product_events WHERE app_version <> ''
),
counted AS (
  SELECT project, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u
  FROM vals GROUP BY project, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT *, ROW_NUMBER() OVER (
    PARTITION BY project, day, event_name, attr_key
    ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT r.project, r.day, r.event_name, r.attr_key, r.attr_value, r.c, r.u
FROM ranked r, cap WHERE r.rn <= cap.n
UNION ALL
-- The tail collapses to one row whose unique_users is recomputed from raw
-- rather than summed, matching rollupAttr's second statement.
SELECT v.project, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id)
FROM vals v, cap
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project = v.project AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value AND r.rn <= cap.n)
GROUP BY v.project, v.day, v.event_name, v.attr_key;
```

The `ORDER BY c DESC, attr_value` tiebreak must match `rollupAttr`'s `ORDER BY c DESC, v` exactly, or the two halves disagree on which values fall in the tail. Build the view in two passes — declared keys first, then the system dimensions — re-running the invariant test after each.

- [ ] **Step 4: Seed the cap into `meta` at boot**

In `internal/app/app.go`, after `Migrate`, `SetMeta(ctx, "product_attributes_top_n", strconv.Itoa(cfg.ProductAttributesTopN))`. The view reads it, so it must be present before the first query.

- [ ] **Step 5: Point Evidence and MCP at the view**

Replace the Evidence source with a plain `select * from v_product_attrs`, dropping the sentinel-row workaround and its comment. The page's `attr_breakdowns` query changes table name only. `tools_product.go` queries `v_product_attrs` instead of `agg_product_attrs`.

- [ ] **Step 6: Run the suite**

Run: `make check`
Expected: PASS, including the pre-existing view invariant tests.

If replicating top-N in the live half proves unreasonable, take the spec's documented fallback: let the live half report full cardinality, drop this task's invariant assertion to cover only keys under the cap, and note the divergence in `docs/configuration.md`. Record that as a ruling.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "fix(store): stop attribute breakdowns lagging behind every other surface

agg_product_attrs was the only aggregate table with no stitch view, so
breakdowns appeared only once a day aggregated - up to raw_days stale
while v_product_daily showed today."
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/configuration.md:73,88-108`
- Modify: `internal/server/ingest.go:124-128` (stale comment)
- Modify: `internal/mcpserver/guide.go`, `internal/mcpserver/docs_content.go`
- Modify: `README.md` if it mentions `product_aggregation`

- [ ] **Step 1: Rewrite the projects table row and the aggregation section**

Replace the `product_aggregation` row with `attributes`, described as: the keys that become `attr_*` columns in `v_events_flat` and get value breakdowns in `agg_product_attrs`. Document that everything sent is still stored and undeclared keys stay reachable through the `attributes` JSON column. Replace the old JSON example with `"attributes": ["plan", "tier"]`, document `PRODUCT_ATTRIBUTES_TOP_N` as the global cardinality cap guarding client-supplied values, and state that `$platform` and `$app_version` roll up automatically without being declared.

Document the alias rule (`^[a-z0-9]+$`, immutable, changed only via `project rename`) in the `alias` row, and add `project rename` plus `-attr` to the CLI examples.

- [ ] **Step 2: Fix the stale ingest comment**

`internal/server/ingest.go:126-127` says unknown reserved keys are dropped because keeping one "would add a column to v_events_flat for a typo". That reason no longer holds — only declared keys become columns. Restate the actual reason: unknown `$` keys are dropped because the `$` namespace is reserved for system fields and an unrecognised one is a client bug.

- [ ] **Step 3: Verify the docs match the code**

Run: `make check` — `internal/mcpserver/docs_sync_test.go` enforces that the embedded MCP docs match the files.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs: document declared attributes, the alias rule and project rename"
```

---

## Verification

Run `make check` from the repo root. Then, against a scratch database:

1. `twillingate project create -alias blog -attr plan` — succeeds.
2. `twillingate project create -alias My-Blog` — rejected by the charset rule.
3. Send a product event carrying `plan`, an undeclared `junk` key, and `$platform`.
4. `SELECT attr_plan, json_extract(attributes,'$.junk') FROM v_events_flat` — `attr_plan` is a column, `junk` is reachable but has no column.
5. `SELECT DISTINCT attr_key FROM v_product_attrs WHERE project='blog'` — includes `plan` and `$platform`.
6. `twillingate project rename -alias blog -to journal` — data and ingest keys follow.
