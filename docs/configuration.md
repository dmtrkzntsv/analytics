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
  -origin https://myapp.com
twillingate project list
twillingate project update -alias myapp -origin https://myapp.com -origin https://www.myapp.com
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
| `alias` | Internal key: the `project` column on every stored row and the dashboard label. Never transmitted. |
| `name` | Display name. |
| `identity` | `anonymous` (default) or `identified`. See the README's Privacy and GDPR section. |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required — `twillingate key issue` mints and registers one in a single step. |
| `allowed_origins` | Origins allowed to post for this project. `*` is a wildcard — `https://*.example.com` covers every subdomain, a bare `*` allows any origin. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `retention` | Per-project override of any retention window. |
| `product_aggregation` | Opt-in product rollup, see below. |

`retention` and `product_aggregation` are not plain CLI flags — set them
through `twillingate config export` (dumps every project as JSON) and
`twillingate config import FILE` (upserts from that same JSON, or from a
pre-upgrade `projects.json` — it detects the legacy bare-array format
automatically). Import never archives or deletes anything absent from the
file, so it is safe to import a partial edit.

Product aggregation is off unless enabled. Attribute breakdowns are opt-in
per key, `"*"` applies to every event, and only the top `top_n` values per
attribute are kept — the rest collapse into a single `(other)` row whose
unique-user count is computed from the raw data rather than summed:

```json
"product_aggregation": {
  "enabled": true,
  "attributes": { "*": ["plan"], "subscribed": ["tier", "source"] },
  "top_n": 50
}
```

Retention overrides are field-level, so a project can keep raw hits longer
without restating the rest:

```json
"retention": { "web": { "raw_days": 90 } }
```

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
