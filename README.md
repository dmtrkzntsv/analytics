# analytics

Cookieless web analytics and opt-in product event tracking in a single Go
binary backed by SQLite. It answers "how many people visited, from where, and
what did they do" without cookies, without a consent banner, and without a
database server. One process handles ingestion, aggregation and retention; a
static Evidence site renders the dashboards.

Privacy follows the Plausible model rather than the Google Analytics one.
Visitors are identified by `hash(daily_salt, ip, user_agent, project)`, the
salt is regenerated every 24 hours and the old one destroyed, and neither the
IP address nor the full User-Agent is ever written to the database or the
logs — they exist only long enough to compute the hash. Cross-day linking of a
visitor is therefore not merely disallowed but impossible after rotation.

## Quickstart — all-in-one

Ingestion, backup and dashboards on one machine (a Raspberry Pi is enough):

```bash
git clone <this repo> && cd analytics/backoffice
cp ../deploy/config.example.json config.json   # edit: projects, allowed_origins
printf 'R2_BUCKET=…\nR2_ENDPOINT=…\nR2_ACCESS_KEY=…\nR2_SECRET_KEY=…\n' > .env
docker compose -f docker-compose.aio.yml up -d
open http://localhost:3000        # dashboards; ingestion is on :8080
```

Put Caddy, nginx or a Cloudflare tunnel in front of `:8080` for TLS.

## Quickstart — split (VPS + backoffice)

Ingestion on a public VPS, dashboards at home off a replica.

On the VPS:

```bash
make build
sudo ./deploy/install.sh              # prompts for a service account
sudo vi /etc/analytics/config.json    # projects, allowed_origins
sudo vi /etc/analytics/litestream.env # R2 credentials
sudo systemctl start analytics litestream
```

On the backoffice machine:

```bash
cd backoffice
cp ../deploy/config.example.json config.json   # set sync.replica_path=/data/replica.db
docker compose up -d
```

`analytics sync` restores the database from R2 into a temporary file, runs
`PRAGMA quick_check` on it and only then swaps it into place, so a failed or
corrupt restore leaves the previous replica serving.

## Embedding

```html
<script defer src="https://analytics.example.com/js/script.js" data-project="myapp"></script>
```

Pageviews are sent automatically, including on `history.pushState` and
`popstate`, so single-page apps work without extra code. Add
`data-user="<id>"` to attribute product events to a known user.

```js
analytics.track("signup", { plan: "pro" });  // opt-in product event
analytics.identify("user-123");              // set the user id at runtime
```

The snippet stays silent on localhost, `file://` URLs and automated browsers.
To exclude yourself:

```js
localStorage.analytics_ignore = "true";
```

### Server-side events

Product events do not need the browser. The endpoint takes JSON and returns
`202`:

```bash
curl -X POST https://analytics.example.com/api/event \
  -H 'Content-Type: application/json' \
  -d '{"project":"myapp","name":"subscribed","user_id":"user-123",
       "attributes":{"plan":"pro"}}'
```

`/api/hit` requires an `Origin` matching the project's `allowed_origins`;
`/api/event` does not, so it can be called from a backend. Request bodies are
capped at 16 KiB.

## Configuration

All keys live in one JSON file (`/etc/analytics/config.json`); see
`deploy/config.example.json`.

| Key | Meaning |
| --- | --- |
| `listen` | Address to bind. Default `127.0.0.1:8080`. |
| `database` | Store DSN. Only `sqlite://<path>` today. |
| `geo` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `log.level` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `log.format` | `json` or `text`. Default `json`. |
| `log.file` | Log to this path instead of stdout. |
| `buffer.flush_max_events` | Flush once this many events are buffered. Default 1000. |
| `buffer.flush_interval` | Flush at least this often. Default `5s`. |
| `buffer.capacity` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `retention.web.raw_days` | Days raw hits are kept before rollup. Default 7. |
| `retention.web.aggregate_days` | Days aggregates are kept. Default 365. |
| `retention.product.raw_days` | Days raw events are kept before rollup. Default 30. |
| `retention.product.aggregate_days` | Days product aggregates are kept. Default 365. |
| `sync.interval` | Replica refresh cadence for the `sync` command. Default `5m`. |
| `sync.litestream_config` | Litestream config passed to `litestream restore`. |
| `sync.replica_path` | Where the verified replica is swapped into place. |
| `projects[].alias` | Stable key used in `data-project`, payloads and every stored row. |
| `projects[].name` | Display name. |
| `projects[].allowed_origins` | Origins allowed to post hits for this project. |
| `projects[].retention` | Per-project override of any `retention` field. |
| `projects[].product_aggregation` | Opt-in product rollup, see below. |

Product aggregation is off unless enabled. Attribute breakdowns are opt-in per
key, `"*"` applies to every event, and only the top `top_n` values per
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

## Privacy and GDPR

- No cookies and no `localStorage` writes, so no consent banner is required
  for the pageview tracking on its own.
- Visitors are `hash(daily_salt, ip, user_agent, project)`. The salt rotates
  every 24 hours and the previous value is overwritten, which makes linking a
  visitor across days impossible rather than just prohibited.
- IP addresses and full User-Agents are never stored or logged. They are used
  to compute the hash, to classify device/browser/OS, and to look up a country
  code, then discarded. A test asserts this by scanning the raw database file
  and the log output for them.
- Query strings are stripped to a UTM allowlist before storage, so tokens and
  personal data in URLs do not get persisted.
- Referrers are reduced to a source name; full referrer URLs are not kept.
- Bots are classified and dropped at ingestion.

Two responsibilities remain with the operator. Paths are stored verbatim, so a
URL scheme that embeds personal data (`/users/jane@example.com/settings`) will
put that data in the database — strip or rewrite it before it reaches the
tracker. And `user_id` on product events is yours to choose: passing an email
address makes the event data personally identifying, with the consent and
erasure obligations that follow.

## Raspberry Pi and low-resource hosts

The defaults suit a small VPS. On a Pi or anything memory-constrained:

- Raise `buffer.flush_interval` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval` in `deploy/litestream/litestream.yml`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `geo` on `cloudflare://` or `none://`; the MaxMind provider downloads
  and holds a database in memory.

Maintenance is bounded on purpose: aggregation and pruning run once a day at
03:00 UTC, and free pages are reclaimed with incremental vacuum rather than a
full `VACUUM`, which would rewrite the whole file.

## Dashboards

Evidence serves a static site on `:3000`: an index of projects, and a web and
product page for each. It reads the database directly (all-in-one) or the
verified replica (split), and rebuilds when the replica changes, otherwise
every few minutes.

Dashboards never show a seam between raw and aggregated data. Every figure
comes from a `v_*` stitch view that unions the aggregate tables with a live
computation over whatever raw rows have not been rolled up yet, so today's
traffic and last year's appear in one series.

## Development

```bash
make test        # go test -race
make check       # vet + coverage gate: >=80% total, >=85% for core packages
make build       # single binary
make build-all   # linux amd64 / arm64 / arm
make docker      # container image
```

Tests are written first; every commit is expected to leave `make check`
green.
