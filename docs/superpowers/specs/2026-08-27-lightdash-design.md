# Lightdash as an alternative to Evidence — design

Status: **declined, 2026-08-27. We will not implement this.** The document is
kept as the record of why, so the question does not get re-opened from scratch.
Nothing here is pending work, and no part of it should be picked up as a task.

Decision: stay on Evidence. Lightdash cannot read SQLite (§2.1), so adoption
costs an export pipeline and a Postgres instance purely as workaround; its
agentic half is Enterprise-gated on self-host (§2.2); and the metric governance
that motivated the look is now available in Evidence itself (§11). The one
requirement that would justify revisiting is **non-technical self-serve
exploration**, which Evidence's OSS core genuinely does not offer.

Date: 2026-08-27. Revised after reading `docs.lightdash.com/llms.txt` in full.

## 1. What Lightdash means by "BI as code"

Two independent layers, both YAML in Git, both driven by `@lightdash/cli`.

**Semantic layer.** Models, dimensions, metrics, joins. Normally these live in a
dbt project's `schema.yml` under `meta:`, but dbt is *optional*: "Lightdash YAML"
([semantic-layer/yaml](https://docs.lightdash.com/semantic-layer/yaml)) uses the
same vocabulary hoisted to the top level, pointing straight at warehouse tables.

```yaml
# lightdash/models/web_daily.yml
type: model
name: web_daily
sql_from: 'analytics.public.agg_web_daily'
dimensions:
  - name: day
    sql: ${TABLE}.day
    type: date
metrics:
  pageviews:
    type: sum
    sql: ${TABLE}.pageviews
```

with `lightdash/lightdash.config.yml` declaring `warehouse: {type: postgres}`,
deployed by `lightdash lint` then `lightdash deploy --create
--no-warehouse-credentials` (credentials are entered once in the UI, never
committed).

**Content layer.** Charts, dashboards, spaces — and agents and roles — round-trip
as YAML via `lightdash download` / `lightdash upload [--force]`:

```
lightdash/
├── spaces/<name>.space.yml
├── charts/<slug>.yml          # metricQuery + chartConfig (eCharts)
└── dashboards/<slug>.yml      # tiles referencing chart slugs, filters, layout
```

`.lightdash-metadata.json` is local state and gets gitignored.

**Virtual views** are a third, useful escape hatch: a raw `SELECT` promoted to a
reusable explore, defined by `slug`/`name`/`sql`/`columns` and managed with
`lightdash upload --virtual-views <slug>`. They exist only in Lightdash and are
never written back to dbt.

**CI shape.** On merge to main: `lightdash deploy` then `lightdash upload
--force`. On PRs: `lightdash start-preview`. Secrets: `LIGHTDASH_API_KEY`,
`LIGHTDASH_URL`, `LIGHTDASH_PROJECT`.

## 2. Two blocking constraints

### 2.1 Lightdash cannot read SQLite

Lightdash is a **server**, not a static site generator. Self-hosting requires a
PostgreSQL instance for its own metadata, an S3-compatible bucket for the full
feature set, and **a warehouse connection that is not SQLite**. Supported:
BigQuery, Postgres, Supabase, Redshift, Snowflake, Databricks, Trino, ClickHouse,
DuckDB (MotherDuck or DuckLake only), Athena.

The docs *do* say "SQLite", and it is easy to misread. Verbatim, from the
DuckLake section of connect-project:

> **SQLite** — "A SQLite file on the Lightdash server. Provide the absolute path
> to the catalog file."

That is the **catalog backend**: the metadata store where DuckLake records
tables, schemas and snapshots, in a file DuckLake creates and owns. The rows
themselves live at a separate **data path** backend (S3 / GCS / Azure / local
filesystem) as Parquet. No field accepts an existing SQLite database of yours.

This collides head-on with the repo's thesis — README, first paragraph:
"without a database server", one Go binary, a Raspberry Pi is enough.

### 2.2 The agentic story is Enterprise-gated on self-host

This is the correction that matters most, because "Agentic BI" is Lightdash's
headline and it is what makes the tool interesting for a Claude Code user.
From [self-host/enterprise-features](https://docs.lightdash.com/self-host/enterprise-features),
each of these needs `LIGHTDASH_LICENSE_KEY`, obtained by scheduling a call with
the enterprise team:

| Feature | Self-hosted OSS |
|---|---|
| **MCP server** (`/api/v1/mcp`) | ❌ Enterprise |
| **AI agents / AI Analyst** | ❌ Enterprise |
| **Pre-aggregates**, incl. *external* pre-aggregates | ❌ Enterprise |
| **Metrics SQL API** (Postgres wire protocol, port 5432) | ❌ Enterprise |
| Data apps, AI writeback | ❌ Enterprise |
| Custom roles, service accounts, SCIM, enterprise SSO | ❌ Enterprise |
| Semantic layer YAML, content-as-code, CLI, previews, explore UI, dashboards, spaces, virtual views, user attributes | ✅ MIT core |
| `lightdash install-skills` | ✅ — a local CLI that writes `.claude/skills/`; no instance, no key |

Two consequences specific to this project:

- **External pre-aggregates would have been a perfect fit and is unavailable.**
  "Route pre-aggregate queries to a warehouse table you build and refresh
  yourself" describes this repo exactly — it *is* a pre-aggregation engine. Anyone
  skimming the docs index will spot it and get excited. It is Enterprise.
- **The agent story reduces to authoring, not querying.** `install-skills` drops
  Lightdash conventions into `.claude/skills/` so Claude Code writes valid model
  and chart YAML — genuinely useful, and free. But pointing an agent at the
  *data* through MCP requires a licence or Lightdash Cloud.

And Cloud is not a clean escape here: it means shipping the analytics data
off-box, which is the one thing this project's design exists to avoid.

## 3. Telemetry — a posture conflict to settle explicitly

A self-hosted instance sends a RudderStack event stream to
`https://analytics.lightdash.com` **by default**: installation context, user
actions, query metrics, AI usage. It does *not* send warehouse credentials, SQL
text, query results, dashboard configs or integration secrets — so this is usage
telemetry, not data exfiltration, and it is fair to say so. Community Edition
never contacts `api.keygen.sh` (that is licence validation, Enterprise only).
Sentry is off unless `SENTRY_BE_DSN` / `SENTRY_FE_DSN` are set.

Still: a project whose README leads with "neither the IP address nor the full
User-Agent is ever written to the database or the logs" cannot ship a dashboard
tier that phones home on by default. So

```
RUDDERSTACK_ANALYTICS_DISABLED=true
```

on **backend, scheduler and worker** containers is a mandatory line in the
compose file, not a documented option — and the reasoning goes in
`docs/lightdash.md`.

## 4. Warehouse options

| | How | Cost | Verdict |
|---|---|---|---|
| **A1. `sqlite_scan` back door** | A virtual view or `sql_from` carrying `select * from sqlite_scan('/data/replica.db', 'agg_web_daily')` on a DuckDB connection, against a bind-mounted replica. | None. No export, no Parquet, no Postgres, no `internal/warehouse`. | Undocumented, may not work at all. Test first — it reshapes everything. |
| **A2. DuckLake** | Export aggregates to Parquet on local disk; DuckLake catalog in a SQLite file. Docs bless a SQLite catalog + local-filesystem data path for single-pod installs. | No warehouse server, but an export step. Newest connector; docs mention a dbt ≥1.8 / `dbt-duckdb` dependency whose applicability to standalone YAML is unclear. | Attractive, unproven. |
| **B. Postgres mirror** *(recommended fallback)* | Go export copies `agg_*`, `projects`, `identities`, `actors` from the SQLite snapshot into Postgres. SQLite stays the source of truth; ingestion untouched. | One extra database **on the Postgres instance Lightdash already forces you to run** — marginal cost ≈ zero. Adds the repo's first real runtime dependency (`pgx`). | Best-supported connector, smallest blast radius. |
| **C. Postgres store backend** | Register `postgres://` in `store.Open`; run ingestion on Postgres. | ~25 interface methods, all the SQLite window-function SQL in `002_views.sql` / `004_app_views.sql` rewritten, and it abandons "no database server" for everyone. | No. B is the stepping stone if this is ever wanted. |

**Order of attack: A1, then A2, then B.** A1 is an afternoon and either collapses
most of the plan or is ruled out cleanly. B is the thing that definitely works.

## 5. Architecture

### 5.1 Renderer seam

`internal/dashboards` hardwires Evidence today: snapshot → `npm run sources` →
`npm run build` → atomic slot rotation → `http.FileServer`. Introduce:

```go
// internal/dashboards
type Renderer interface {
    // Refresh brings the published dashboards up to date with snap.
    Refresh(ctx context.Context, snap string) error
    // Handler serves the dashboards, or nil if something else does.
    Handler() http.Handler
}
```

- `evidenceRenderer` — the existing `Builder` behind the interface, unchanged.
- `lightdashRenderer` — `Refresh` publishes the snapshot to the warehouse (or,
  under A1, just swaps the replica file). `Handler` returns a stub that 302s to
  `LIGHTDASH_SITE_URL` and serves `/healthz`, because Lightdash serves its own UI.

`run.go`'s poll/stamp/interval loop and the "keep the last good build up on
failure" property are renderer-agnostic and stay exactly as they are. Selected by
`DASHBOARDS_RENDERER=evidence|lightdash` (default `evidence`).

### 5.2 Export package (only if A1 and A2 both fail)

`internal/warehouse`, `Export(ctx, snapshotPath, dsn) error`. Full refresh per
table in one transaction — `TRUNCATE` + `COPY FROM`. The aggregate tables are
small (one row per project × day × dimension value) and a full refresh sidesteps
every incremental-sync correctness question. Schema generated from the SQLite
`CREATE TABLE`s, not hand-maintained, so a new migration cannot silently desync.

The stitch **views** are not exported: they union aggregates with un-aggregated
raw rows, and raw rows carry `visitor_hash`, which has no business leaving the
collector. Consequence, and it must be documented: **Lightdash shows data only up
to the aggregation boundary, so today is missing until the 03:00 pass runs.**
Evidence shows it today. If that is unacceptable, export view output instead —
decide in Phase 0.

This path needs **no Node at runtime**, so `analytics dashboards` in lightdash
mode runs in the slim ~35 MB `runtime` image; the 1 GB `analytics-evidence` image
is not involved. `lightdash deploy` runs in CI or by hand, never in the container.

## 6. Porting the semantic layer

21 Evidence source queries and 8 page templates become ~21 models and 6
dashboards.

**Models** (`lightdash/models/`), one per aggregate table: `web_daily`,
`web_pages`, `web_referrers`, `web_countries`, `web_devices`, `web_browsers`,
`web_os`, `web_utm`, `app_daily`, `app_screens`, `app_countries`, `app_devices`,
`app_os`, `app_versions`, `product_daily`, `product_totals`, `product_attrs`,
`retention`, `identity_daily`, plus `projects` and `identities` as join targets.

Shared dimensions `project` (joined to `projects.name`) and `day`; per-model
`path`, `source`, `country`, `device`, `browser`, `os`, `utm_*`, `screen`,
`app_version`, `event_name`, `user_id`/`group_id`, `cohort_day`/`day_offset`.

### The additive-uniques trap — the highest-risk item in the port

`agg_web_daily.visitors` is *already* a distinct count, per day. A Lightdash
metric `sum(visitors)` over 30 days returns the sum of 30 daily uniques — not
unique visitors, and materially larger. Evidence dodges this because a human
wrote each page and chose the range; Lightdash's entire point is that users slice
freely. The codebase already knows the hazard: `agg_product_totals` is commented
"preserves true DAU".

So:

- name the additive metric honestly — `daily_visitors_sum`, described as "sum of
  daily unique visitors; double-counts returning visitors across days";
- expose a real `unique_visitors` as `count_distinct` over the `actors` table,
  which exists for exactly this and is pruned to the retention window — correct
  inside the window, unavailable outside it. State that boundary in the metric
  description, where Lightdash surfaces it in the UI.

[Writing useful descriptions](https://docs.lightdash.com/semantic-layer/writing-descriptions)
is the relevant reference; descriptions are the only place this caveat can live
where a self-serve user will actually see it. Getting this wrong produces
confidently wrong numbers, which is worse than no dashboard at all.

### Per-project scoping

Six page templates × N projects becomes **one** set of six dashboards with a
required `project` dashboard filter. For access control, prefer
[user attributes](https://docs.lightdash.com/workspace-admin/user-attributes)
(row-level, `project` attribute per user) over one space per project — custom
roles are Enterprise, user attributes are not.

### Things that get deleted

- `evidence/svelte.config.js` — 60 lines of prerender-entry generation that
  exists only because SvelteKit cannot crawl DuckDB-WASM-resolved links. The
  `N projects × 6 routes` explosion goes away with the dashboard filter.
- The sentinel-row empty-database guards in all 21 source `.sql` files.
- The Dockerfile's DuckDB-WASM extension warm-up step.

## 7. Privacy and access control

Users/groups/retention are meaningful only for `identity: identified` projects;
filter those models on `identity = 'identified'` and scope them by user attribute.

Worth stating plainly in the docs: the README's consent-free posture concerns
*ingestion* and is unchanged by any of this. But Evidence's output is a static
site with no accounts, whereas Lightdash is a multi-user application with logins,
saved queries and optional scheduled deliveries, holding per-user analytics data
— plus the telemetry stream of §3. That is a new surface with new obligations. Not
a blocker; it does not inherit the existing privacy story for free.

## 8. Packaging

`deploy/compose/docker-compose.lightdash.yml`:

```
postgres     # lightdash metadata (+ the analytics warehouse database, under option B)
lightdash    # SITE_URL, LIGHTDASH_SECRET, RUDDERSTACK_ANALYTICS_DISABLED=true
dashboards   # ghcr.io/dmtrkzntsv/analytics:latest, DASHBOARDS_RENDERER=lightdash
```

Realistic footprint is several GB of RAM against Evidence's static output, so the
Pi quickstart in the README stays on Evidence and the image table gains a row
explaining which tier is which.

**CI wrinkle to decide early.** The documented workflow assumes GitHub Actions can
reach the Lightdash instance. For a home or single-VPS deploy it cannot. Options:
a self-hosted runner, a Cloudflare tunnel, or skip CI and ship
`make lightdash-deploy` / `make lightdash-upload` for a human to run against a
port-forward. The last is the honest default for this project's audience.
`lightdash lint` needs no instance and can gate PRs regardless.

## 9. Phases

**Phase 0 — spike (time-boxed, ~1 day).** Stand up Lightdash on docker-compose
with telemetry disabled. Then, in order:

1. Test the A1 back door: bind-mount a seeded replica, create a virtual view with
   `sqlite_scan()`, see whether it explores. Likely failure modes: extension
   autoload blocked, read-only container filesystem, Lightdash wrapping `sql_from`
   in a way that breaks table-valued functions.
2. If A1 fails, try A2 (DuckLake, SQLite catalog + local Parquet) and settle
   whether standalone YAML really needs `dbt-duckdb`.
3. If both fail, load Postgres by hand and confirm `deploy --create
   --no-warehouse-credentials` works.

Also settle the tables-vs-views question from §5.2 and confirm a required project
filter behaves.
*Kill criteria: if none of A1/A2/B yields a rendering chart, or if the OSS feature
set in §2.2 is judged too thin, stop here — Phases 1–6 all rest on this.*

**Phase 1 — `internal/warehouse`** (skipped entirely if A1 works). TDD against a
Postgres testcontainer or a `WAREHOUSE_TEST_DSN`-guarded skip consistent with
existing test style. Schema generation from the SQLite catalogue, full-refresh
export, idempotence, and a test proving a new migration is picked up. Adds `pgx`.
Must clear the 85% coverage floor.

**Phase 2 — renderer seam.** Extract `Renderer`, move `Builder` behind it
unchanged, add `lightdashRenderer`, wire the new config through. Existing
`dashboards` tests must pass untouched — that is the regression proof for Evidence.

**Phase 3 — semantic layer.** `lightdash.config.yml` and ~21 model files. Resolve
the uniques modelling per §6. `lightdash lint` in CI.

**Phase 4 — content.** Six dashboards with a required project filter. Run
`lightdash install-skills` so Claude Code authors charts against the governed
metrics. Spot-check every number against the corresponding Evidence page on the
same seeded database — this is where §6 gets verified, not assumed.

**Phase 5 — packaging and docs.** Compose file, `docs/lightdash.md` (including §3
and the OSS/EE table), README image table and a "which dashboard tier" section,
Makefile targets.

**Phase 6 — CI (optional).** `lightdash lint` on PRs always; `deploy` + `upload
--force` only once §8's reachability problem is solved.

## 10. Trade-off summary

**Gained:** self-serve exploration for people who will not edit markdown;
free multi-user accounts, spaces and user attributes (Evidence OSS has none);
no 15-minute rebuild staleness; preview environments and `lightdash validate`;
the prerender hack and sentinel guards deleted.

**Lost:** static output with no server and no accounts; the ~35 MB /
Raspberry-Pi story for the dashboard tier; Evidence's narrative markdown pages
(Lightdash markdown tiles are much weaker); today's partial data unless §5.2
exports views; the repo's zero-runtime-dependency Go module; and — the big one —
**MCP, AI agents and pre-aggregates, all Enterprise on self-host.**

**Therefore:** add Lightdash behind the renderer seam as an opt-in tier and keep
Evidence the default. But be clear-eyed that the OSS self-hosted feature set is
"a nicer explore UI with free multi-user access control", not "agentic BI" — the
agentic half needs a sales call or Lightdash Cloud, and Cloud means the analytics
data leaves the box, which is the premise this project was built to avoid.

**The one requirement that justifies this port is non-technical self-serve.**
Nothing else in the Gained list is worth Phases 1–5. If that requirement is not
real, stop at Phase 0 — or before it, per §11.

## 11. Cheaper alternative: Evidence Metrics

Checked 2026-08-27: Evidence has shipped a metrics layer since the v40 pinned in
`evidence/package.json`. `metrics/*.yaml` declares a metrics view — a base table
or `base_sql`, an optional date column, named dimensions, and named aggregations
with their own filters, formatting and labels. Calculated metrics reference
others with single braces (`{revenue} / nullif({orders}, 0)`), and metric filters
compile to `FILTER (WHERE …)` aggregates rather than query-level `WHERE`, so
metrics with divergent filters coexist in one query. Pages consume them with
`metric="name"` on a data component, inheriting aggregation, format and label.

This matters because **§6's additive-uniques trap — the highest-risk item in the
whole port — is fixable inside Evidence today.** Define `daily_visitors_sum` and
a true `unique_visitors` over `actors` once in `metrics/*.yaml`, with the
retention-window caveat in the label, instead of trusting eight page templates to
get the aggregation right independently. That is an afternoon against a stack
that already works, versus Phases 0–4 here.

What it does *not* give you is the self-serve explorer: Evidence's Explore
interface, viewer chat, access rules and row-level security are all
[Evidence Studio](https://evidence.dev/blog/evidence-studio), the hosted tier,
and the Evidence Agent is Studio-only — the mirror image of Lightdash's
Enterprise gating. Neither project ships agentic BI in its open-source core.

**Do this first.** Upgrade Evidence past v40, move the aggregation rules into
`metrics/*.yaml`, and re-ask whether the remaining gap is really "we need a
self-serve BI server" or was just "our metric definitions were scattered".
