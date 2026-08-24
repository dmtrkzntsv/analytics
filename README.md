# analytics

Cookieless web analytics, app analytics and opt-in product event tracking in a
single Go binary backed by SQLite. It answers "how many people visited, from
where, and what did they do" — on the web and inside desktop and mobile apps —
without a database server. One process handles ingestion, aggregation and
retention; a static Evidence site renders the dashboards.

Privacy follows the Plausible model rather than the Google Analytics one. By
default every project is **anonymous**: identifiers are salted with a key that
rotates every 24 hours and the old one is destroyed, so linking a visitor
across days is impossible rather than merely prohibited. Neither the IP
address nor the full User-Agent is ever written to the database or the logs.

A project can opt into **identified** mode, which stores the identifiers you
supply as given and unlocks retention cohorts and per-user reporting. That
mode writes a persistent `localStorage` id on the web, which is
terminal-equipment storage under ePrivacy — the same legal category as a
cookie. **The consent-free posture holds for anonymous projects and does not
hold for identified ones.** See [Privacy and GDPR](#privacy-and-gdpr).

Apps talk to the same endpoint as the web. See
[docs/ingest-api.md](docs/ingest-api.md) for the normative wire format —
batching, offline replay, client timestamps and retry semantics.

## Quickstart — one server

Ingestion and dashboards on one machine (a Raspberry Pi is enough). No
credentials, no object storage, no checkout:

```bash
mkdir analytics && cd analytics
base=https://raw.githubusercontent.com/dmtrkzntsv/analytics/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
curl -fsSL $base/projects.example.json -o projects.json   # edit: projects, allowed_origins
docker compose up -d
open http://localhost:3000        # dashboards; ingestion is on :8080
```

Put Caddy, nginx or a Cloudflare tunnel in front of `:8080` for TLS.

Dashboards come up on `:3000` as `503` while Evidence runs its first build —
about a minute — then serve the site. Backup is opt-in and separate: see
[docs/litestream.md](docs/litestream.md).

## Quickstart — two servers

Ingestion on a public VPS, dashboards at home off a replica. The bucket is
the only channel between them; neither machine needs to reach the other.

On the VPS — either straight from a GitHub release:

```bash
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/deploy/install.sh | sudo bash
```

(add `-s -- --yes` for a non-interactive install, `-s -- --version v0.2.0` to
pin; while the repository is private, pass a token:
`curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" …/install.sh | sudo GITHUB_TOKEN="$GITHUB_TOKEN" bash`)

or from a checkout:

```bash
make build
sudo ./deploy/install.sh              # prompts for a service account
```

then in both cases:

```bash
sudo vi /etc/analytics/projects.json  # projects, allowed_origins
sudo vi /etc/analytics/analytics.env  # R2 credentials, geo
sudo systemctl start analytics litestream
```

The remote form downloads the release tarball for the machine's architecture
(amd64/arm64/arm), verifies it against the release's `SHA256SUMS`, and installs
binary, config, systemd units and logrotate rules. Releases are published by
CI whenever a `v*` tag is pushed.

On the machine that renders dashboards, install the restore script on cron
and point the dashboards at what it produces:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
sudo install -m 0755 restore.sh /usr/local/bin/restore.sh   # from deploy/litestream/
sudo cp restore.cron /etc/cron.d/analytics-restore
docker compose -f docker-compose.evidence.yml up -d
```

`restore.sh` restores from R2 into a temporary file, runs `PRAGMA quick_check`
on it and only then renames it into place, so a failed or corrupt restore
leaves the previous replica serving. The collector itself never talks to
object storage — [docs/litestream.md](docs/litestream.md) covers the whole
arrangement.

## Images

| Image | Contents | Modes |
| --- | --- | --- |
| `ghcr.io/dmtrkzntsv/analytics` | the binary on alpine, ~35 MB | `serve`, `migrate`, `version` |
| `ghcr.io/dmtrkzntsv/analytics-evidence` | the binary, Node 22 and the Evidence project, ~1 GB | `dashboards` |

They are separate because `serve` is the internet-facing process and has no
business carrying a Node toolchain. Both are published for amd64 and arm64 on
every `v*` tag; the collector is also published for 32-bit arm.

Updating is `docker compose pull && docker compose up -d`. Never `down -v` —
the database lives in the named volume.

## Embedding

```html
<script defer src="https://analytics.example.com/js/script.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

Run `analytics keygen` to generate the key and print this snippet ready to
paste. `data-identity` mirrors the project's `identity` setting so a mismatch
is visible at a glance; the server enforces the real value regardless, so a
wrong tag can waste a storage write but never leak a raw identifier.

Pageviews are sent automatically, including on `history.pushState` and
`popstate`, so single-page apps work without extra code. Add
`data-user="<id>"` and `data-group="<id>"` when the page is rendered already
knowing who is looking at it.

```js
analytics.track("signup", { plan: "pro" });      // opt-in product event
analytics.identify("user-123", "org-9");         // set user and group at runtime
analytics.reset();                               // on logout — see below
```

Call `analytics.reset()` when a user logs out. Without it the next person to
use a shared browser inherits the previous user's identity. Events already
sent stay unattributed: there is no retroactive stitching, so a pageview
fired before `identify()` keeps no user id.

The snippet stays silent on localhost, `file://` URLs and automated browsers.
To exclude yourself:

```js
localStorage.analytics_ignore = "true";
```

### Apps and server-side events

Everything goes to one endpoint, `POST /api/events`. The `name` decides where
it lands: `$pageview` is a web pageview, `$screen_view` an app screen view,
anything else a custom event. `$`-prefixed attribute keys carry system
context; everything else is yours.

```bash
curl -X POST https://analytics.example.com/api/events \
  -H 'Content-Type: application/json' \
  -H 'X-Analytics-Key: ak_9f3c…' \
  -d '{"attributes":{"$platform":"ios","$app_version":"2.4.1","$install_id":"018f…"},
       "events":[
         {"id":"018f…","ts":"2026-08-23T10:00:00Z",
          "name":"$screen_view","attributes":{"$screen":"/settings"}},
         {"id":"018f…","ts":"2026-08-23T10:00:05Z",
          "name":"subscribed","attributes":{"plan":"pro"}}]}'
```

Supply a UUIDv7 `id` per event and a batch retried after a timeout dedupes
server-side; omit it and a replay double-counts. Client `ts` values are
honoured and clamped to the raw-retention window, so a device offline for
three weeks replays with correct timestamps.

If an `Origin` header is present it must match the project's
`allowed_origins`; native apps send none and are unaffected. Bodies are capped
at 256 KiB and 500 events per batch.

Full contract, including retry semantics and a worked offline-queue design:
[docs/ingest-api.md](docs/ingest-api.md).

## Configuration

Infra settings are environment variables. On installed hosts both systemd
units load them from `/etc/analytics/analytics.env` (`EnvironmentFile=`);
the binary itself only reads the real environment. See
`.env.example` at the repo root.

| Variable | Meaning |
| --- | --- |
| `LISTEN_ADDR` | Address to bind. Default `127.0.0.1:8080` (the docker image sets `0.0.0.0:8080`). |
| `DATABASE_URL` | Store DSN. Only `sqlite://<path>` today. Required. |
| `GEO_URL` | Country lookup: `cloudflare://` (header), `maxmind://<license-key>`, or `none://`. |
| `PROJECTS_FILE` | Path of the projects file. Default `/etc/analytics/projects.json`. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error`. Default `info`. |
| `LOG_FORMAT` | `json` or `text`. Default `json`. |
| `LOG_FILE` | Log to this path instead of stdout. |
| `BUFFER_FLUSH_MAX_EVENTS` | Flush once this many events are buffered. Default 1000. |
| `BUFFER_FLUSH_INTERVAL` | Flush at least this often. Default `5s`. |
| `BUFFER_CAPACITY` | Bounded queue size; excess is dropped rather than growing memory. Default 10000. |
| `RETENTION_WEB_RAW_DAYS` | Days raw hits are kept before rollup. Default 7. |
| `RETENTION_WEB_AGGREGATE_DAYS` | Days aggregates are kept. Default 365. |
| `RETENTION_PRODUCT_RAW_DAYS` | Days raw events are kept before rollup. Default 30. |
| `RETENTION_PRODUCT_AGGREGATE_DAYS` | Days product aggregates are kept. Default 365. |
| `DASHBOARDS_DB_PATH` | Database `dashboards` renders. Defaults to the `DATABASE_URL` path. |
| `DASHBOARDS_ADDR` | Address the dashboards bind. Default `0.0.0.0:3000`. |
| `DASHBOARDS_INTERVAL` | Minimum spacing between Evidence rebuilds. Default `15m`. |
| `DASHBOARDS_PROJECT_DIR` | Evidence project in the image. Default `/opt/evidence`. |
| `DASHBOARDS_WORK_DIR` | Where the database snapshot is written. Default `/var/lib/dashboards`. |

If you replicate with litestream, its credentials
(`LITESTREAM_ACCESS_KEY_ID`, `LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`,
`R2_ENDPOINT`) live in the same `analytics.env`, so secrets never sit in a
JSON file. Nothing in the collector reads them.

Projects live in `projects.json` — a JSON array (see
`projects.example.json` at the repo root):

| Key | Meaning |
| --- | --- |
| `alias` | Internal key: the `project` column on every stored row and the dashboard label. Never transmitted. |
| `name` | Display name. |
| `identity` | `anonymous` (default) or `identified`. See [Privacy and GDPR](#privacy-and-gdpr). |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required. |
| `allowed_origins` | Origins allowed to post for this project. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `retention` | Per-project override of any retention window. |
| `product_aggregation` | Opt-in product rollup, see below. |

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

### Ingest keys

A project needs at least one key, and the key identifies the project — no
payload carries a project field. Multiple keys let a website, an iOS app and a
desktop app be retired on their own schedules:

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
Deleting the entry is the eventual cleanup; the flag keeps the step reversible
during a botched rollout.

## Privacy and GDPR

The posture depends on the project's `identity` mode.

### `anonymous` (default)

- No cookies and no `localStorage` writes, so no consent banner is required
  for the pageview tracking on its own.
- Identifiers are salted with a key that rotates every 24 hours; the previous
  value is overwritten, which makes linking across days impossible rather than
  just prohibited. Web pageviews hash the connection
  (`hash(daily_salt, ip, user_agent, project)`); apps hash the `$install_id`
  they supply, which is more accurate — a shared carrier NAT no longer merges
  many people into one "visitor" — at identical privacy.
- `$user_name` is ignored entirely: storing a person's name against a
  daily-rotating hash would defeat the anonymisation.
- IP addresses and full User-Agents are never stored or logged. They are used
  to compute the hash, to classify device/browser/OS, and to look up a country
  code, then discarded. A test asserts this by scanning the raw database file
  and the log output for them.
- Query strings are stripped to a UTM allowlist before storage, so tokens and
  personal data in URLs do not get persisted.
- Referrers are reduced to a source name; full referrer URLs are not kept.
- Bots are classified and dropped at ingestion (pageviews only — app traffic
  is never filtered on User-Agent).

### `identified`

- Identifiers you supply are stored **as given**, and the web snippet writes a
  persistent `localStorage` visitor id.
- That id is terminal-equipment storage under ePrivacy — the same legal
  category as a cookie, whatever the technical mechanism. **The consent-free
  claim above does not hold for these projects.** Consent gating is yours:
  withhold `data-identity="identified"` until the visitor has consented.
- In exchange you get retention cohorts, D1/D7/D30 curves and per-user
  reporting, none of which are definable under daily rotation.
- Display names (`$user_name`) are stored. They are personal data.

### Both modes

`$group_id` and `$group_name` are stored raw in both modes: a group
identifies an organization, not a natural person, and hashing it would make
dashboards unreadable for no real privacy gain. If your groups are
single-person, treat them as personal data.

Two responsibilities remain with the operator. Paths are stored verbatim, so a
URL scheme that embeds personal data (`/users/jane@example.com/settings`) will
put that data in the database — strip or rewrite it before it reaches the
tracker. And `user_id` on product events is yours to choose: passing an email
address makes the event data personally identifying, with the consent and
erasure obligations that follow.

## Raspberry Pi and low-resource hosts

The defaults suit a small VPS. On a Pi or anything memory-constrained:

- Raise `BUFFER_FLUSH_INTERVAL` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval` in `deploy/litestream/litestream.yml`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `GEO_URL` on `cloudflare://` or `none://`; the MaxMind provider
  downloads and holds a database in memory.

Maintenance is bounded on purpose: aggregation and pruning run once a day at
03:00 UTC, and free pages are reclaimed with incremental vacuum rather than a
full `VACUUM`, which would rewrite the whole file.

## Dashboards

`analytics dashboards` serves a static Evidence site on `:3000`: an index of
projects, and web, app, product, users, groups and retention pages for each.
The app page's version adoption chart is the one to watch a release roll out
on. The users and retention pages are only meaningful for `identified`
projects and say so otherwise; groups work in both modes.

Evidence is a static site generator — it bakes query results in at build time
— so serving dashboards is a build loop. Each cycle copies the database with
`VACUUM INTO` and renders that snapshot, so the Node process never opens the
file the collector is writing (or that a restore job is about to replace).
It rebuilds when the database has changed and at most once per
`DASHBOARDS_INTERVAL`, and a build that fails leaves the previous site
serving.

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
make dist        # release tarballs (binary + deploy/) with SHA256SUMS
make docker      # both container images
```

Tests are written first; every commit is expected to leave `make check`
green.

### Testing the service locally

```bash
make run          # build and serve on 127.0.0.1:8080 with local/.env + local/projects.json
make smoke        # boot the real binary, POST a batch, verify rows land
make test-install # run deploy/install.sh in a Debian container, assert artifacts
make test-compose # build both images, run the compose stack, assert a rendered page
make seed-demo    # fill local/analytics.db with 180 days of demo traffic
make dashboards   # Evidence dev server against local/analytics.db (port 3000)
make clean        # remove binary, dist/, local/ state
```

`make run` writes a dev config on first use (`local/.env` +
`local/projects.json`: project `dev`, localhost origins, `GEO_URL=none://`,
debug text logs, database at `local/analytics.db`) — edit both freely, they
are never overwritten. Exercise it
with a browser UA (curl's default is classified as a bot and dropped):

```bash
curl -X POST localhost:8080/api/events -A 'Mozilla/5.0' \
  -H 'Origin: http://localhost:8080' -H 'Content-Type: application/json' \
  -H 'X-Analytics-Key: <key from local/projects.json>' \
  -d '{"events":[{"name":"$pageview",
                  "attributes":{"$url":"http://localhost/some-page"}}]}'
```

`make dashboards` builds its parquet extracts from whatever is in the database
at that moment, so run `make seed-demo` (or send some hits) first -- otherwise
the charts have nothing to plot. `seed-demo` covers every project in
`local/projects.json` (each gets its own traffic profile) over 180 days, and
needs the server to have started once so the projects are registered.

Both dashboards share one date-range selector -- 1, 7, 30, 90 or 180 days,
defaulting to 7 -- that drives every widget on the page. The windows are
anchored to UTC, matching how hits are stored; DuckDB's `current_date` resolves
in the viewer's local timezone, which west of UTC would silently drop the
current day.

Paths in "Top pages" link through to a per-page breakdown. Note that Evidence
interpolates `${...}` into the SQL text verbatim, so any value taken from the
URL has to be escaped before it reaches a query -- see the comment in
`pages/web/[project]/page.md`. Charts cannot be drilled into: the ECharts click
event is not forwarded to a component a markdown page can reach, so drill-downs
have to start from a table.

`make smoke` is the end-to-end check: scratch database, real HTTP ingestion
covering all three event kinds plus bad-key, bad-Origin and replay-dedupe
cases, then row counts straight from SQLite.
`make test-install` runs the installer in a throwaway container with
`systemctl` stubbed and asserts users, permissions, units, config, logrotate,
and that re-running preserves an edited config.

### Releasing

Push a tag and CI builds the tarballs and publishes the GitHub release that
`curl … install.sh` consumes:

```bash
git tag v0.1.0 && git push origin v0.1.0
```
