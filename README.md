# twillingate

Cookieless web, app and product analytics in a single Go binary backed by
SQLite. One process handles ingestion, aggregation and retention; a static
Evidence site renders the dashboards; an optional MCP endpoint answers the
same questions in plain language.

Every project is **anonymous** by default — identifiers are salted with a
key that rotates every 24 hours and the old one is destroyed, and neither
the IP address nor the full User-Agent is ever stored. Projects can opt into
**identified** mode for retention cohorts and per-user reporting, which
writes persistent `localStorage` state and so is consent-relevant under
ePrivacy. See [Privacy and GDPR](#privacy-and-gdpr).

## Run it

Ingestion and dashboards on one machine (a Raspberry Pi is enough):

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
open http://localhost:3000        # dashboards; ingestion is on :8080
```

Put Caddy, nginx or a Cloudflare tunnel in front of `:8080` for TLS.
Dashboards answer `503` for about a minute while Evidence runs its first
build. Updating is `docker compose pull && docker compose up -d`; never
`down -v`, the database lives in the named volume. Images are
`ghcr.io/dmtrkzntsv/twillingate` (collector, ~35 MB, amd64/arm64/arm32) and
`ghcr.io/dmtrkzntsv/twillingate-evidence` (dashboards, carries Node).

For anything beyond a hobby install, **split tracking from reporting**: run
ingestion on a small public host and the dashboards elsewhere off a restored
Litestream replica, so the bucket is the only channel between them. Reporting
outages then cannot cost you events, and the collector needs no public
dashboard. [docs/deployment.md](docs/deployment.md) is the runbook for that
two-machine setup; [docs/litestream.md](docs/litestream.md) covers the
replication and the backup drills.

## Track something

`twillingate key issue` prints this ready to paste:

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

Pageviews are automatic, SPAs included. The same file is a full SDK for
web, product and app analytics from code:

```js
twillingate.track("signup", { plan: "pro" });   // product event
twillingate.screen("/settings");                // app screen view
twillingate.identify("user-123", "Ada");        // identified projects
twillingate.group("org-9", "Acme Corp");
twillingate.reset();                            // on logout
```

Native apps and backends skip the SDK and POST batches straight to
`/api/events`. Details: [docs/sdk.md](docs/sdk.md) and
[docs/ingest-api.md](docs/ingest-api.md).

## Ask it questions (MCP)

With `MCP_AUTH_DSN` set (`token://…`, `cloudflare://…?aud=…` or
`oauth://…`), `serve` also exposes an authenticated MCP endpoint at `/mcp`:
read tools, a guarded SQL `query` tool, management tools and an
`integration_guide` that returns paste-ready setup per project and
platform. Connect Claude (or any MCP client) and ask "how many visitors
did myapp get last week, by country" instead of opening dashboards.
Setup per auth mode: [docs/mcp-auth.md](docs/mcp-auth.md).

## Documentation

| Doc | Covers |
| --- | --- |
| [docs/deployment.md](docs/deployment.md) | Systemd install, splitting tracking from reporting, upgrades, backup drills, disaster recovery, routine ops |
| [docs/configuration.md](docs/configuration.md) | Every environment variable, projects, ingest keys, retention, low-resource tuning |
| [docs/sdk.md](docs/sdk.md) | twillingate.js: snippet mode, SDK-only mode, runtime API, offline queue |
| [docs/ingest-api.md](docs/ingest-api.md) | The normative wire format for `/api/events` |
| [docs/mcp-auth.md](docs/mcp-auth.md) | MCP auth modes: static token, Cloudflare Access, generic OAuth IdP |
| [docs/litestream.md](docs/litestream.md) | Backup and replication: bucket setup, writer, reader, recovery |
| [docs/migration.md](docs/migration.md) | One-time migration from the old `analytics` binary |
| [docs/plausible/](docs/plausible/) | Shim for Plausible tagged-event classes |

## Privacy and GDPR

The posture depends on the project's `identity` mode.

**`anonymous` (default).** No cookies and no `localStorage` writes, so no
consent banner is required for pageview tracking on its own. Identifiers are
salted with a key that rotates every 24 hours; the previous value is
overwritten, which makes linking across days impossible rather than just
prohibited. IPs and full User-Agents are never stored or logged (a test
asserts this by scanning the database file and log output). Query strings are
stripped to a UTM allowlist, referrers reduced to a source name, bots dropped
at ingestion.

**`identified`.** Identifiers you supply are stored **as given**, and the
SDK writes a persistent `localStorage` visitor id — terminal-equipment
storage under ePrivacy, the same legal category as a cookie. **The
consent-free claim does not hold for these projects**; gate
`data-identity="identified"` on consent.

**Both modes.** `$group_id`/`$group_name` are stored raw (a group is an
organization, not a person). Paths are stored verbatim — strip personal
data from URL schemes before it reaches the tracker. Enabling MCP exposes
identified projects' stored ids to every valid token holder; complete
erasure is `twillingate project delete`, deliberately CLI-only.

## Development

```bash
make check       # what CI runs: vet + coverage gate + restore test
make build       # single binary
make run         # local server on 127.0.0.1:8080 with a dev project
make smoke       # boot the real binary, POST a batch, verify rows land
make seed-demo   # 180 days of demo traffic in local/twillingate.db
make dashboards  # Evidence dev server against the local database
cd sdk && npm ci && npm test   # the twillingate.js SDK suite
```

The SDK bundle is committed (`internal/server/twillingate.js`); after
editing `sdk/src/`, run `npm run build` there and commit the result — CI
fails on drift. Every push to `main` cuts a release tagged
`vYY.MMDD.{build}`. Commit messages follow Conventional Commits and become
the release notes.
