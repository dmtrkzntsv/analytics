# Configuration reference

Infra settings are environment variables. On installed hosts both systemd
units load them from `/etc/twillingate/twillingate.env` (`EnvironmentFile=`);
docker compose reads a `.env` next to the compose file; the binary itself
only reads the real environment. `.env.example` at the repo root ships every
variable with its default.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `LISTEN_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `PUBLIC_URL` | The collector's public base URL (`https://twillingate.example.com`). Embed snippets, MCP integration guidance and the default MCP resource URL are built from it; unset, they carry a placeholder. |
| `DATABASE_DSN` | Store DSN. Only `sqlite://<path>` today. Required. |
| `GEO_DSN` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `RETENTION_WEB_RAW_DAYS` | Days raw hits are kept before rollup. Default 7. |
| `RETENTION_WEB_AGGREGATE_DAYS` | Days aggregates are kept. Default 365. |
| `RETENTION_PRODUCT_RAW_DAYS` | Days raw product events are kept before rollup. Default 30. |
| `RETENTION_PRODUCT_AGGREGATE_DAYS` | Days product aggregates are kept. Default 365. |
| `PRODUCT_ATTRIBUTES_TOP_N` | Distinct attribute values kept per (project, day, event, key) in `agg_product_attrs` before the rest collapse into `(other)`. Default 50. See [Attribute breakdowns](#attribute-breakdowns). |
| `RETENTION_APP_RAW_DAYS` | Days raw app events are kept before rollup. Default 30. |
| `RETENTION_APP_AGGREGATE_DAYS` | Days app aggregates are kept. Default 365. |
| `DASHBOARDS_DB_PATH` | Database `dashboards` renders. Defaults to the `DATABASE_DSN` path. |
| `DASHBOARDS_ADDR` | Address the dashboards bind. Default `0.0.0.0:3000`. |
| `DASHBOARDS_INTERVAL` | Minimum spacing between Evidence rebuilds. Default `15m`. |
| `DASHBOARDS_PROJECT_DIR` | Evidence project in the image. Default `/opt/evidence`. |
| `DASHBOARDS_WORK_DIR` | Where the database snapshot is written. Default `/var/lib/dashboards`. |
| `MCP_AUTH_DSN` | MCP authentication: `token://<token>`, `cloudflare://<team>?aud=<tag>` or `oauth://<issuer-host>`. Unset, bare `serve` skips MCP with a warning. See [mcp-auth.md](mcp-auth.md). |
| `MCP_ADDR` | Give the MCP endpoint its own listener. Defaults to `LISTEN_ADDR` (shared). |
| `MCP_DB_PATH` | Database MCP reads for queries. Defaults to the `DATABASE_DSN` path. |
| `MCP_QUERY_TIMEOUT` | Per-query guard on the MCP `query` tool. Default `10s`. |
| `MCP_QUERY_MAX_ROWS` | Row cap on the MCP `query` tool. Default 1000. |

If you replicate with litestream, its credentials
(`LITESTREAM_ACCESS_KEY_ID`, `LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`,
`R2_ENDPOINT`) live in the same `twillingate.env`, so secrets never sit in a
JSON file. Nothing in the collector reads them.

## Projects

Projects live in the database — a registry table, not a file — and are
managed entirely through the CLI (or an MCP management tool):

```bash
twillingate project create -alias myapp -name "My App" -identity anonymous \
  -origin https://myapp.com -attr plan -attr tier
twillingate project list
twillingate project update -alias myapp -origin https://myapp.com -origin https://www.myapp.com
twillingate project rename -alias oldname -to newname  # data and ingest keys follow
twillingate project archive -alias myapp    # reversible: `project restore` undoes it
twillingate key issue -project myapp -label web
twillingate key list -project myapp
twillingate key disable -project myapp -label ios-2025
```

`project update` (and the `update_project` MCP tool) merge rather than
replace: a field you omit keeps its current value. `-origin` is the one
exception worth knowing — supplying it at all replaces the whole origins
list. Neither `project update` nor the MCP tool can clear origins down to
an empty list (an empty list is treated the same as "not supplied"); to do
that, edit the origins to `[]` in a `config export` dump and
`config import` it back.

Each project record has these fields:

| Key | Meaning |
| --- | --- |
| `alias` | Internal key: the `project` column on every stored row and the dashboard label. Never transmitted. New aliases must match `^[a-z0-9]+$` (lower-case letters and digits only — `blog`, `blog2`, `2048` are fine, `my_app` and `shop-uk` are not). Aliases are immutable once created; change one with `twillingate project rename -alias old -to new`, which rewrites the `project` column across every table in one transaction. Ingest keys follow the rename, so deployed clients keep working without redeploying. An alias created before this rule existed keeps working and can still be edited — only new aliases are checked. |
| `name` | Display name. |
| `identity` | `anonymous` (default) or `identified`. See the README's Privacy and GDPR section. |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required — `twillingate key issue` mints and registers one in a single step. |
| `allowed_origins` | Origins allowed to post for this project. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `retention` | Per-project override of any retention window. |
| `attributes` | Custom attribute keys to break down, see below. |

`retention` is not a plain CLI flag — set it through `twillingate config
export` (dumps every project as JSON) and `twillingate config import FILE`
(upserts from that same JSON, or from a pre-upgrade `projects.json` — it
detects the legacy bare-array format automatically). Import never archives
or deletes anything absent from the file, so it is safe to import a partial
edit. `attributes` can go through the same round-trip, but `-attr` on
`project create`/`project update` is the normal path — see below.

Retention overrides are field-level, so a project can keep raw hits longer
without restating the rest:

```json
"retention": { "web": { "raw_days": 90 } }
```

### Attribute breakdowns

A project declares which product-event attribute keys are worth reporting
on:

```json
"attributes": ["plan", "tier"]
```

or, without touching JSON at all:

```bash
twillingate project update -alias myapp -attr plan -attr tier
```

`-attr` is repeatable and, like `-origin`, replaces the whole list when
supplied at all. Declaring a key drives two things: it gets its own
`attr_*` column in `v_events_flat`, and a value breakdown (counts and
unique users per distinct value, per event, per day) in
`agg_product_attrs` / `v_product_attrs`.

Everything sent is still stored regardless of `attributes`. An undeclared
key has no dedicated column but stays reachable via
`json_extract(attributes, '$.junk')`; declaring a key later does not
backfill past history, only future rollups. Rollups themselves run
unconditionally now — there is no `enabled` flag. Previously a project
that never opted in had its raw product events deleted at
`product.raw_days` with nothing rolled up first, silently losing that
history; a project with no `attributes` declared still gets daily counts
and totals, it just has nothing to break down.

Declaring a key bounds columns, not the values inside one — clients supply
those, and `agg_product_attrs` stores one row per distinct value per key
per event per day. An unbounded-cardinality key like a URL or session id
would make the aggregate grow as fast as the raw data it exists to
summarise, defeating retention. `PRODUCT_ATTRIBUTES_TOP_N` (default 50)
guards against that globally: only the top N values per key are kept, and
the rest collapse into one `(other)` row whose unique-user count is
recomputed from raw data rather than summed.

`$platform` and `$app_version` roll up automatically without being
declared, the same way web and app surfaces have always aggregated their
own system dimensions — they appear in `agg_product_attrs` /
`v_product_attrs` under those `$`-prefixed keys. Do not add them to
`attributes`: `$`-prefixed keys are reserved and never reach the custom
attribute blob, so `"attributes": ["$platform"]` would extract nothing.

## Ingest keys

A project needs at least one key, and the key identifies the project — no
payload carries a project field. Multiple keys let a website, an iOS app
and a desktop app be retired on their own schedules:

```json
"ingest_keys": [
  { "key": "ak_9f3c…", "label": "web" },
  { "key": "ak_2b71…", "label": "ios" },
  { "key": "ak_5d04…", "label": "ios-2025", "disabled": true }
]
```

Keys are public by design — they ship in app binaries and page source — so
their job is revocation and project identification, not secrecy. To retire
one: add the replacement, ship clients, watch the old label fall to zero in
the per-minute `ingest summary` log line, then set `"disabled": true`.
Deleting the entry is the eventual cleanup; the flag keeps the step
reversible during a botched rollout.

## Raspberry Pi and low-resource hosts

The defaults suit a small VPS. On a Pi or anything memory-constrained:

- Raise `BUFFER_FLUSH_INTERVAL` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval` in `deploy/litestream/litestream.yml`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `GEO_DSN` on `cloudflare://` or `none://`; the MaxMind provider
  downloads and holds a database in memory.

Maintenance is bounded on purpose: aggregation and pruning run once a day
at 03:00 UTC, and free pages are reclaimed with incremental vacuum rather
than a full `VACUUM`, which would rewrite the whole file.
