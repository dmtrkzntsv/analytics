# twillingate

Web, app and product analytics as one Go binary and one SQLite file —
cookieless and anonymous by default ([details](#privacy-and-gdpr)). It holds
about 15 MB of memory, needs no database server and no cluster, and is happy on
a Raspberry Pi from day one. An MCP endpoint means your coding agent can set it
up and answer questions about it, so the dashboards are there when you want
them rather than being the point.

**Ask your agent to integrate it.** With MCP enabled, "set up analytics for
this app" is the whole task: the agent creates the project, issues an ingest
key and asks `integration_guide` for paste-ready setup for your platform, then
wires it into your code itself.

**Then ask it for the numbers.** "How many visitors did myapp get last week, by
country" beats clicking through a dashboard, and read tools plus a guarded SQL
`query` tool answer it. Run the Evidence dashboards too if you like charts —
that is a second compose file, and skipping it costs you nothing else.

## Run it

Tracking is one file: ingestion, the tracker script and — once `MCP_AUTH_DSN`
is set, see [The MCP endpoint](docs/deployment.md#the-mcp-endpoint)
— the MCP endpoint, all on
`:8080`:

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose
curl -fsSLO $base/docker-compose.yml
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
```

That prints a snippet to paste; an agent with MCP access can do the same two
steps for you. Put Caddy, nginx or a Cloudflare tunnel in front of `:8080` for
TLS.

Dashboards are a second file, whenever you want them:

```bash
curl -fsSLO $base/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml > .env
docker compose up -d          # dashboards on :3000, 503 for a minute while Evidence builds
```

The `COMPOSE_FILE` line saves repeating `-f` on every later command. Beyond a
hobby install, keep tracking and reporting apart: reporting reads a Litestream
replica rather than the live database, so a reporting outage cannot cost you
events. Each file ships the litestream service it needs, commented out.
[docs/deployment.md](docs/deployment.md) is the runbook.

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
`/api/events`. Details: [Instrument a website](docs/twillingate.md#instrument-a-website)
and [The wire format](docs/twillingate.md#the-wire-format).

## Documentation

Two pages. [docs/twillingate.md](docs/twillingate.md) is how you *use*
twillingate, and is what the MCP endpoint serves to agents as
`docs://twillingate` — the text you read and the text they read are the same
bytes. [docs/deployment.md](docs/deployment.md) is how you *run* it.

| Section | Covers |
| --- | --- |
| [Configure](docs/twillingate.md#configure) | Every environment variable, projects, ingest keys, retention, attribute breakdowns, low-resource tuning |
| [Instrument a website](docs/twillingate.md#instrument-a-website) | twillingate.js: snippet and SDK modes, URL masking, routing modes, offline queue |
| [The event model](docs/twillingate.md#the-event-model) | The three event families, for native apps and backends too |
| [The wire format](docs/twillingate.md#the-wire-format) | The normative contract for `/api/events` |
| [Use it from an MCP client](docs/twillingate.md#use-it-from-an-mcp-client) | The tools and resources a connected session gets |
| [Query the data](docs/twillingate.md#query-the-data) | The views and the caveats needed to write correct SQL |
| [Install](docs/deployment.md#install) | systemd and docker compose, verifying ingestion |
| [The MCP endpoint](docs/deployment.md#the-mcp-endpoint) | Auth modes, and pointing Claude Code, Desktop or claude.ai at it |
| [Operate and recover](docs/deployment.md#operate-and-recover) | Upgrades, litestream replication, backup drills, disaster recovery |
| [docs/plausible/](docs/plausible/) | The Plausible class-tagging shim |

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
