# Declared product attributes

Date: 2026-08-29
Status: approved, pending implementation plan

## Problem

Product attributes arrive as free-form JSON on `product_events.attributes`.
Two consumers expand them, and both are driven by whatever clients happened
to send rather than by anything the operator chose:

- `v_events_flat` (built at runtime by `internal/store/sqlite/flatview.go`)
  gets one `attr_*` column per key returned by `KnownAttributeKeys`, which is
  `SELECT DISTINCT je.key FROM product_events, json_each(attributes)` across
  every project. One site's typo becomes a column for every site, forever.
- `agg_product_attrs` gets rollups for the keys named in each project's
  `product_aggregation.attributes`.

So the same concept is configured twice, in two different shapes, and the
view half is not configured at all. Tracking many sites makes this worse on
three axes the operator named: the view widens without bound, it is awkward
to query, and it is rebuilt from a full table scan on boot and nightly.

## Decision

**Attributes are declared, not discovered.** Ingest keeps storing everything
it receives; views and aggregations only surface keys the operator named.

That single change removes the column explosion at its source, deletes the
scan, and collapses two config concepts into one.

## 1. Configuration

`product_aggregation` is replaced by one project field:

```json
"attributes": ["plan", "tier", "source"]
```

Three things go away.

**The `enabled` flag.** It did not mean "skip aggregation" — it meant
"delete without aggregating". `AggregateProductDay`
(`internal/store/sqlite/aggregate_product.go:33-39`) deletes the day's raw
rows whether or not it is set; `enabled` only decided whether anything was
rolled up first. Since the default is nil, every project that never opted in
silently discarded product history at `product.raw_days`, and
`v_product_daily` went empty for those days. Rollups now always run.

**The event-name map.** `{"*": ["plan"], "subscribed": ["tier"]}` becomes a
flat list. Per-event scoping bought nothing: `rollupAttr` already filters
`AND json_extract(attributes, :path) IS NOT NULL`
(`aggregate_product.go:109` and `:134`), so a key absent from an event
produces zero rows, not junk. The map prevented a problem the NULL filter
already prevents.

**`top_n`.** It is a safety rail against client-supplied *value*
cardinality, not a per-site tuning knob — the operator picks the key, but
clients pick the values, and `agg_product_attrs` stores one row per
(project, day, event_name, attr_key, attr_value). It becomes a global
setting, `PRODUCT_ATTRIBUTES_TOP_N`, default 50, read via `e.num` alongside
the `RETENTION_PRODUCT_*` family. A per-project override can be added later
if a site ever needs one.

### Storage and migration

`projects.product_aggregation` is replaced by `projects.attributes TEXT NOT
NULL DEFAULT '[]'`. The migration backfills the new column with the DISTINCT
union of every array in the old map, then drops the old column in the same
transaction (the runner already wraps each migration in one — `migrate.go`).

`config import` continues to accept the legacy `projects.json` shape,
converting `product_aggregation.attributes` to the union list on the way in,
so the documented upgrade path keeps working. `config export` emits the new
shape.

The MCP `update_project` tool loses its `aggregationIn` struct
(`internal/mcpserver/tools_manage.go:36-45`) in favour of a flat list.

## 2. CLI

`attributes` becomes a first-class flag instead of requiring the
`config export` -> edit JSON -> `config import` round-trip that
`docs/configuration.md:88` documents today:

```
twillingate project update -alias blog -attr plan -attr tier
```

`-attr` is repeatable and replaces the whole list when supplied, matching
the documented `-origin` semantics.

## 3. `v_events_flat`

Stays a single shared view. Columns:

- `id`, `project`, `event_name`, `actor_id`, `ts`
- `attributes` — the raw JSON, so undeclared keys stay reachable via
  `json_extract` and the view is never a downgrade from the base table
- one `attr_<key>` per key in the union of all projects' declared lists,
  sorted for deterministic ordering

`KnownAttributeKeys` and its full table scan are deleted from `store.Store`.
`RebuildFlatView(ctx, keys []string)` keeps its signature; only the source
of `keys` changes, from a scan of `product_events` to the registry snapshot
via a new `func (s *Snapshot) DeclaredAttributeKeys() []string` that owns
the union logic in one testable place.

Rebuilt on registry mutation (so a config edit takes effect at once), at
boot (`internal/app/app.go:94`), and in the daily pass
(`internal/jobs/jobs.go:139`) as a repair net. A `pragma_table_info`
comparison skips the DROP/CREATE when the column set is unchanged, so the
daily pass is a no-op in steady state.

`sanitizeAlias` (`flatview.go:37`) stays on the key-to-column path.
Declared keys are operator-authored rather than client-supplied now, so it
is defence in depth rather than the primary guard, but it is what stands
between a fat-fingered config and broken DDL.

## 4. Alias rule

`^[a-z0-9]+$`, enforced in `ProjectSpec.validate()`
(`internal/manage/ops.go:32`) and so inherited by create, import-create and
rename.

`UpdateProject` deliberately does **not** charset-check: there the alias is
a selector for an existing row, not a proposed name. Without the exception,
an operator holding a legacy `my_app` could not edit it or round-trip
`config export | config import`, locking them out of the command that fixes
it.

The rule is hygiene, not safety — nothing splices an alias into DDL. The
CLI's bare `project %q not found` (`cmd/twillingate/project.go:132`) gains
an explicit note that aliases are immutable, pointing at `project rename`
and `project create`.

## 5. `project rename`

```
twillingate project rename -alias old -to new
```

Validates `new` against the charset and rejects it if taken, then one
transaction: `UPDATE projects SET alias=?` erroring on zero rows affected,
`UPDATE <t> SET project=?` across `projectTables`
(`internal/store/sqlite/registry.go:182`), then `auditAndBump` with action
`project.rename` and subject `old->new`, which bumps `config_version` so
other processes reload.

`ingest_keys` is in `projectTables`, so keys follow the rename and deployed
clients keep working. `PRAGMA foreign_keys` is never enabled, so the
`REFERENCES projects(alias)` clause is not enforced and update order does
not matter; `projects` goes first regardless.

CLI only, not exposed over MCP: it rewrites every table keyed by the
project, which does not belong on the agent-facing surface.

## 6. `v_product_attrs`

`agg_product_attrs` is the only aggregate table with no stitch view, so
Evidence reads it directly. Like every other source in
`evidence/sources/twillingate/`, it works around the sqlite connector's
empty-table failure with a sentinel row — that guard is the house pattern,
identical byte-for-byte across all 21 sources, and stays. What is specific
to this source is one comment sentence explaining *why* the table might be
empty: "The attribute-breakdown rollup job may not have run yet, leaving
this table empty" (`evidence/sources/twillingate/agg_product_attrs.sql:1`).
The practical effect behind that sentence is that attribute breakdowns
only materialise when a day is aggregated, at `today - product.raw_days`
(`internal/jobs/jobs.go:104`) — so with `raw_days: 30`, `v_product_daily`
shows today while attribute breakdowns are up to a month stale.

Adding `v_product_attrs` fixes that lag and removes the now-inaccurate
sentence — the empty-database sentinel guard itself carries over unchanged
onto the new view. It is the most intricate piece of this change, because
`002_views.sql:5-8` states
an invariant: live halves must be numerically identical to what the
aggregation writes, or a dashboard jumps the moment a day ages over. So the
live half has to replicate the top-N-plus-`(other)` collapsing from
`rollupAttr`, over the declared key list.

It can nonetheless be static SQL in a migration rather than a runtime-
generated view, because both inputs are reachable from SQL. The declared
keys live in the new `projects.attributes` JSON column, so the view joins
`product_events` to `projects` on `project = alias` and expands the keys
with `json_each(projects.attributes)`, extracting each value with a runtime
path expression (`json_extract(pe.attributes, '$."' || je.value || '"')`).
The cap is an environment setting and so is not known at migration time; the
app writes it to the existing `meta` table at boot and the view reads it
with a scalar subquery. If replicating top-N proves
unreasonable in practice, the documented fallback is a live half that
reports full cardinality, with a note that recent days are complete while
aged days are top-N — a difference invisible for any key with fewer than
`PRODUCT_ATTRIBUTES_TOP_N` distinct values.

## 7. System dimensions roll up automatically

Web and app surfaces already aggregate every system dimension with no
configuration: `$url` feeds `agg_web_pages` and `agg_web_utm`, `$screen`
feeds `agg_app_screens`, `$app_version` feeds `agg_app_versions`, and so on.

Product events are the exception. `003_app.sql:33-34` added `platform` and
`app_version` columns to `product_events`, and they are written on every
event, but nothing rolls them up — so they are queryable in the raw window,
appear in `v_events_flat`, and then vanish at `product.raw_days` with no
aggregate behind them. They are the only stored product data with no
retention path.

Declaring them is not an option: `resolveAttributes`
(`internal/server/ingest.go:131-138`) routes `$`-prefixed keys to typed
fields and drops unknown ones, so nothing `$`-prefixed ever reaches the
JSON blob. `attributes: ["$platform"]` would extract NULL forever, and
`["platform"]` would mean a different, genuinely custom key.

So they roll up automatically, matching web and app. This needs **no new
tables**: `agg_product_attrs` is already
`(project, day, event_name, attr_key, attr_value, count, unique_users)`,
which is exactly the shape required. `rollupProduct` writes the system
columns into it with `$`-prefixed keys — `attr_key='$platform'`,
`attr_value='ios'` — alongside the declared custom keys, unconditionally and
independent of the `attributes` list.

The `$` prefix namespaces them safely: since a `$` key can never be a custom
key, collision is impossible by construction. They inherit `v_product_attrs`,
the `product_attributes` MCP tool and the Evidence breakdown table for free,
and `PRODUCT_ATTRIBUTES_TOP_N` caps `app_version` cardinality, which grows
without bound over a product's life.

The alternative — dedicated `agg_product_platforms` and
`agg_product_versions` tables mirroring `agg_app_versions` — is more
literally consistent with the web and app families, but costs two tables,
two views and two Evidence sources, and every future typed column would need
the same treatment.

## Rejected alternatives

**Per-project views (`v_events_<alias>`).** Justified only while columns
came from discovery and genuinely diverged per project. Once keys are
declared, the column set is a small operator-controlled list and `attr_sku`
being NULL for `blog` is a value-level fact — which is what `WHERE` is for.
Splitting would also have been the only per-project object in the schema,
while all 13 MCP query sites parameterise `WHERE project=?` and cannot
parameterise an identifier. Reversible: if the shared view becomes
unwieldy, the split is contained work, and by then the need would be known.

**Per-project views over the aggregate tables.** Same reasoning, plus
`sqlite_master` sweep logic and a second source of view truth (migrations
define the shared views; a runtime generator would define the twins) that
would drift on any future migration.

**Physically splitting the aggregate tables per project.** No schema
divergence to justify runtime DDL, per-project migrations, and much heavier
delete/rename paths.

**A database file per project.** The strongest case for it is not
attributes but `db.SetMaxOpenConns(1)` (`sqlite.go:50`), which serialises
every read and write in the system through one connection — the real
scaling ceiling for many sites. It would also reduce deletion to `rm` and
rename to `mv`, retiring the hand-maintained `projectTables` list. Rejected
*for this change*, not on the merits: Evidence connects to exactly one file
(`evidence/sources/twillingate/connection.yaml`), `SQLITE_MAX_ATTACHED`
defaults to 10 so cross-project queries could not simply attach everything,
migrations would need per-database versioning, and the registry must stay
central because ingest resolves a key before it knows the project. It
deserves its own spec driven by the connection ceiling. Nothing here blocks
it; the declared-attributes work ports directly.

## Testing

- Declared-only columns: a key that is stored but undeclared must not appear
  as a column, and must still be reachable through `attributes`.
- Existing hostile-key cases in `flatview_test.go` carried over intact.
- Unchanged column set leaves the view untouched (no DROP/CREATE).
- A config edit takes effect immediately via the registry-mutation trigger.
- Rollups run for a project with no `attributes` declared, so raw deletion
  never destroys daily/totals history.
- Migration backfill: old event-keyed map to DISTINCT union, `top_n`
  dropped, legacy `projects.json` import converted.
- `project rename` preserves row counts across every table in
  `projectTables` and leaves ingest keys valid.
- `v_product_attrs` live and aggregated halves agree across the boundary
  (the invariant `views_test.go` already enforces for other surfaces).
- `$platform` and `$app_version` rows appear in `agg_product_attrs` for a
  project that declares no attributes at all, and survive raw deletion.
- A custom key cannot collide with a system one: an event sending both
  `$platform` and a custom `platform` yields distinct `attr_key` rows.
