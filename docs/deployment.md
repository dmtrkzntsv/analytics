# Deployment

The operator's runbook: getting twillingate onto a host, keeping it backed
up, and getting it back after the host is gone.

Everything about *using* it — setting up a project, instrumenting a site,
the wire format, answering questions from the data — lives in
[twillingate.md](twillingate.md). The two are separate because an operator
standing up a VPS and an agent asking for last week's numbers want
different things.

Both are served over MCP, this one as `docs://deployment`, so an agent
helping with an install reads the same bytes you do.

Two topologies. **One server** puts ingestion and dashboards on the same
machine — simplest, and enough for a Pi at home behind a tunnel. **Two
servers** runs ingestion on a public VPS and dashboards at home off a
restored replica, so the dashboard machine never needs to be reachable from
the internet.

Replication is not part of the application: `serve` writes a SQLite file and
`dashboards` reads one. The two-server topology needs something to move that
file, and litestream is the supported answer — see [Replication with
litestream](#replication-with-litestream). The one-server topology needs
none of it.

- [Install](#install)
- [Configure the collector](#configure-the-collector)
- [Reporting with Evidence](#reporting-with-evidence)
- [The MCP endpoint](#the-mcp-endpoint)
- [Operate and recover](#operate-and-recover) — including litestream for the two-server topology

---

## Install

### docker compose

Tracking: ingestion, the SDK, and `/mcp` once `MCP_AUTH_DSN` is set. The
dashboards are a second compose file, added under [Reporting with
Evidence](#reporting-with-evidence):

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
```

### systemd on a VPS

```bash
# From a published release (no checkout needed):
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash

# Or from a checkout:
git clone <repo> && cd twillingate
make build
sudo ./deploy/systemd/install.sh          # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version vYY.MMDD.{build}` to pin) and verifies
its SHA256 before installing. CI publishes a release on every push to `main`.

The installer creates a system account, installs the binary to
`/usr/local/bin/twillingate`, creates `/var/lib/twillingate` (0750, owned by
the service account), installs an example `twillingate.env` loaded by both
units via `EnvironmentFile=`, renders the systemd units with the chosen user,
and enables them. It never overwrites an existing `twillingate.env`, so
re-running it to deploy a new binary is safe.

Then edit the file it flagged and create your first project — projects live
in the database, not in a shipped file:

```bash
sudo vi /etc/twillingate/twillingate.env
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -alias myapp'
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project myapp -label web'
sudo systemctl start twillingate
curl -s localhost:8080/healthz            # → {"status":"ok"}
```

Put TLS in front of `127.0.0.1:8080` — Caddy, nginx, or a Cloudflare tunnel.
The service binds to loopback by default, deliberately.

### Verifying ingestion

```bash
# Expect 202 and {"accepted":1,...}
curl -i -X POST http://localhost:8080/api/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://myapp.com' \
  -d '{"key":"ak_…","events":[{"name":"$pageview",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'

# Expect 403 — the origin is not in allowed_origins
curl -i -X POST http://localhost:8080/api/events \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://not-allowed.com' \
  -d '{"key":"ak_…","events":[{"name":"$pageview",
       "attributes":{"$host":"myapp.com","$path":"/"}}]}'
```

## Configure the collector

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
| `PRODUCT_ATTRIBUTES_TOP_N` | Distinct attribute values kept per (project, day, event, key) before the rest collapse into `(other)`. Default 50. |
| `RETENTION_APP_RAW_DAYS` | Days raw app events are kept before rollup. Default 30. |
| `RETENTION_APP_AGGREGATE_DAYS` | Days app aggregates are kept. Default 365. |
| `DASHBOARDS_DB_PATH` | Database `dashboards` renders. Defaults to the `DATABASE_DSN` path. |
| `DASHBOARDS_ADDR` | Address the dashboards bind. Default `0.0.0.0:3000`. |
| `DASHBOARDS_INTERVAL` | Minimum spacing between Evidence rebuilds. Default `15m`. |
| `DASHBOARDS_PROJECT_DIR` | Evidence project in the image. Default `/opt/evidence`. |
| `DASHBOARDS_WORK_DIR` | Where the database snapshot is written. Default `/var/lib/dashboards`. |
| `MCP_AUTH_DSN` | MCP authentication: `token://<token>`, `cloudflare://<team>?aud=<tag>` or `oauth://<issuer-host>`. Unset, bare `serve` skips MCP with a warning. |
| `MCP_ADDR` | Give the MCP endpoint its own listener. Defaults to `LISTEN_ADDR` (shared). |
| `MCP_DB_PATH` | Database MCP reads for queries. Defaults to the `DATABASE_DSN` path. |
| `MCP_QUERY_TIMEOUT` | Per-query guard on the MCP `query` tool. Default `10s`. |
| `MCP_QUERY_MAX_ROWS` | Row cap on the MCP `query` tool. Default 1000. |

Litestream credentials (`LITESTREAM_ACCESS_KEY_ID`,
`LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_ENDPOINT`) live in the same
`twillingate.env`, so secrets never sit in a JSON file. Nothing in the
collector reads them.

### Raspberry Pi and low-resource hosts

- Raise `BUFFER_FLUSH_INTERVAL` (for example `30s`) to trade latency for
  fewer, larger writes.
- Raise litestream's `sync-interval`.
- Set `GOMEMLIMIT` (the systemd unit and compose files ship `128MiB`).
- Keep `GEO_DSN` on `cloudflare://` or `none://`; the MaxMind provider
  downloads and holds a database in memory.

Maintenance is bounded on purpose: aggregation and pruning run once a day at
03:00 UTC, and free pages are reclaimed with incremental vacuum rather than a
full `VACUUM`, which would rewrite the whole file.

---

## Reporting with Evidence

`twillingate dashboards` renders an Evidence site from the database. In the
compose setup it is `docker-compose.evidence.yml`, serving port 3000.

It rebuilds within a minute of the database changing — it compares size and
modification time and does not need to be told. `DASHBOARDS_INTERVAL`
(default `15m`) sets the minimum spacing between rebuilds, and each rebuild
snapshots the database into `DASHBOARDS_WORK_DIR`, so the host needs room
for one more copy.

### One server

Add the second file next to the tracking `docker-compose.yml` and let
`COMPOSE_FILE` join them into one project sharing one database:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml >> .env
docker compose up -d
```

`COMPOSE_FILE` is what lets every later `docker compose` command see both
files; without it, pass `-f` twice each time. Port 3000 answers `503` until
Evidence finishes its first build — roughly a minute.

### Two servers

Ingestion on a public VPS, dashboards at home off a restored replica, so the
dashboard machine never needs to be reachable from the internet. The reader
restores the database on cron and renders it:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
docker compose -f docker-compose.evidence.yml up -d
```

Set `DASHBOARDS_DB_PATH=/data/replica.db` in `.env`: the file defaults to
`/data/twillingate.db`, which is the shared-volume case, not this one. The
compose file also carries a commented `restore` service — use it or host
cron, never both.

A failed or corrupt restore is not fatal: `restore.sh` verifies into a
temporary file and only then renames, so the previous replica keeps serving.

## The MCP endpoint

`serve` exposes one MCP endpoint — streamable HTTP at
`https://twillingate.example.com/mcp`. There is no stdio server to install:
every client talks to the running collector over the network.

`twillingate serve -mcp` refuses to run unauthenticated. `MCP_AUTH_DSN` must
be set, and its scheme picks the mode:

```bash
MCP_AUTH_DSN=token://<token>
MCP_AUTH_DSN=cloudflare://<team>.cloudflareaccess.com?aud=<aud-tag>
MCP_AUTH_DSN=oauth://idp.example.com[?resource=<url>][&audience=<aud>]
```

| Mode | Pick it when | Claude Code | Claude Desktop | claude.ai |
| --- | --- | --- | --- | --- |
| `token://` | One operator, no identity provider — the simplest thing that is secure | ✅ `--header` | ⚠️ needs the `mcp-remote` bridge | ❌ cannot send custom headers |
| `cloudflare://` | Your domain is already on Cloudflare | ✅ browser login | ✅ custom connector | ✅ custom connector |
| `oauth://` | You run or rent an IdP (Keycloak, Auth0, Authentik, …) | ✅ browser login | ✅ custom connector | ✅ if the IdP does Dynamic Client Registration |

> **A connected session reads every non-archived project — including
> personal data on `identified` projects — and can use the management
> tools.** There is no per-project scoping. The credential, or the IdP
> behind it, is the whole of the access control. Treat it as an admin
> credential.

Put the MCP surface on its own hostname when you can (`MCP_ADDR` plus a
second DNS name): `/api/events` and the `/js/*` scripts must stay publicly
reachable for ingestion, and a dedicated hostname keeps the access-control
story simple.

### Enabling MCP on an installed host

The installer's unit runs bare `serve`, which starts MCP the moment its auth
is configured — until then it logs `MCP endpoint disabled` at boot. Enabling
it is one edit:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -mcp'
sudo vi /etc/twillingate/twillingate.env    # set MCP_AUTH_DSN
sudo systemctl restart twillingate
```

Set `MCP_ADDR` to give MCP its own port; unset, it shares the ingestion
listener. To run the surfaces as separate processes — independently
restartable and exposable — copy `deploy/systemd/twillingate.service` to
`twillingate-mcp.service`, change its `ExecStart=` to `twillingate serve
-mcp` (explicitly requesting `-mcp` makes missing auth a hard error), and
change the original unit's to `twillingate serve -api`.

**A `serve -mcp`-only process still runs the daily aggregation pass against
`DATABASE_DSN`** — `-mcp` only makes the HTTP listener conditional, not the
background jobs. Point a `-mcp`-only unit at a litestream replica and it
will write to that replica on every pass. Set `MCP_DB_PATH` (what MCP reads)
and `DATABASE_DSN` (what the aggregation pass writes) deliberately: either
keep `DATABASE_DSN` on a database this process is meant to own, or accept
that a two-process topology runs the idempotent daily aggregation twice.

### `token://` — a single static token

```bash
twillingate keygen -mcp        # prints: MCP_AUTH_DSN=token://ar_…
```

The token is a true secret — unlike ingest keys it reads every project and
authorizes the management tools. Rotate by minting a new one and restarting.

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The default scope is `local` — this machine, this project only. Add
`-s user` for every project you open, or `-s project` to write a checked-in
`.mcp.json`. **Do not put the token in a `-s project` config**: `.mcp.json`
is committed. Reference an environment variable instead, expanded at connect
time:

```json
{
  "mcpServers": {
    "twillingate": {
      "type": "http",
      "url": "https://twillingate.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TWILLINGATE_MCP_TOKEN}" }
    }
  }
}
```

For Claude Desktop, the connector UI cannot attach an `Authorization`
header, so a static token needs a local stdio-to-HTTP bridge and Node
installed. Edit `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "twillingate": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://twillingate.example.com/mcp",
               "--header", "Authorization: Bearer ar_…"]
    }
  }
}
```

Then quit Claude Desktop completely — closing the window leaves it in the
tray, and the config is only read at startup. Note the token sits in plain
text and `mcp-remote` is third-party code in the path of an admin
credential. If that is not acceptable, put the MCP hostname behind
Cloudflare Access and use the connector instead.

### `cloudflare://` — Access managed OAuth

Cloudflare Access acts as the OAuth authorization server: it serves the
discovery documents at the edge, runs the browser login against your chosen
identity source, and supports Dynamic Client Registration — which is what
lets claude.ai connect with zero client setup. Your only identity
infrastructure is the Cloudflare account.

1. **Route the MCP hostname through Cloudflare** — orange-cloud DNS or a
   `cloudflared` tunnel to the machine running `serve -mcp`.
2. **Create the Access application**: Zero Trust → Access controls →
   Applications → add a **self-hosted** application for the MCP hostname (or
   hostname + path). Scope it so the ingest endpoints are NOT behind it —
   `/api/events` must stay reachable without an Access session.
3. **Add an Allow policy** — your email via Google/GitHub login, a one-time
   PIN, or any configured IdP. This is your user store.
4. **Enable Managed OAuth** in the application's Advanced settings, and
   enable Dynamic Client Registration (allow-any, or list your client's
   callback in `allowed_uris`). It is opt-in for self-hosted applications.
5. **Copy the application's AUD tag** from the application overview.
6. Set `MCP_AUTH_DSN=cloudflare://yourteam.cloudflareaccess.com?aud=<tag>`.

The token a client holds under managed OAuth is opaque and validated at the
edge. What reaches the origin is the resolved identity as a JWT in the
`Cf-Access-Jwt-Assertion` header; the server validates it against the team's
public keys with the AUD tag as the audience. A request that reaches the
listener without having passed Access carries no valid assertion and is
rejected — an exposed origin port does not bypass Access. In this mode the
binary serves no discovery document and sends no challenge header; the edge
owns both.

Claude Code: add the server without a header, run `/mcp`, choose
*Authenticate*. Claude Desktop and claude.ai: Settings → Connectors → **Add
custom connector**, endpoint `https://twillingate.example.com/mcp`.

### `oauth://` — any standards-compliant IdP

The server is an OAuth 2.1 **resource server** only: it validates the JWTs
your IdP issues, and never sees a password or runs a login page.

The RFC 9728 resource URL defaults to `PUBLIC_URL` + `/mcp` (or pass
`?resource=https://…/mcp`); the expected token audience defaults to that
resource URL (`&audience=…` to override). For a local or plain-http IdP —
development only — use `oauth+insecure://`.

What the IdP must provide, verified before pointing the server at it:

1. **RFC 8414 metadata** at
   `<issuer>/.well-known/oauth-authorization-server` containing a
   `jwks_uri`. The server fetches this once at startup and refuses to boot
   if it is missing:

   ```bash
   curl -s https://auth.example.com/.well-known/oauth-authorization-server | jq .jwks_uri
   ```

   Many IdPs publish only OIDC discovery; most current Keycloak, Auth0 and
   Authentik releases serve the RFC 8414 path too, but confirm rather than
   assume.
2. **Asymmetrically signed access tokens** (RS/ES/PS). HMAC and `alg=none`
   are rejected. If your IdP issues opaque access tokens by default,
   configure it to issue JWTs for this audience.
3. **The audience claim**: tokens must carry `aud` containing the resource
   URL. In most IdPs this means registering the MCP server as an
   API/resource with that identifier and having clients request it.
4. **For claude.ai**: Dynamic Client Registration (RFC 7591), or manually
   register the client and configure its id in the connector.

Key rotation is handled: an unknown `kid` triggers a JWKS refetch, throttled
to once a minute.

### Verifying any mode

```bash
curl -si https://twillingate.example.com/mcp -X POST | head -3
# → HTTP/1.1 401; in oauth mode the WWW-Authenticate header names the
#   discovery document

curl -s https://twillingate.example.com/.well-known/oauth-protected-resource
# → oauth mode: JSON naming your issuer; cloudflare and token modes: 404

curl -s https://twillingate.example.com/healthz
# → {"status":"ok"} — health stays unauthenticated in every mode

claude mcp list          # → twillingate: … - ✓ Connected
```

Anything that speaks streamable HTTP MCP (Cursor, Zed, VS Code, custom SDK
clients) connects to the same URL.

### Troubleshooting a connection

**`401` on connect.** Check the endpoint directly with the curl commands
above. If curl gets a `401` too, the problem is the server or the
credential, not the client.

**Connects but no tools.** Wrong path — the endpoint is `/mcp`, not the bare
hostname.

**Desktop shows nothing after editing the config.** JSON syntax error, or
the app was never fully quit. Node also has to be on the `PATH` the GUI app
inherits, which on macOS is not your shell's; an absolute path to `npx` in
`"command"` settles it.

**Login loops in `oauth://` mode.** The IdP is probably issuing tokens
without the expected `aud`.

**Stale OAuth state.** `mcp-remote` caches under `~/.mcp-auth`; delete it to
force a fresh login.

**Token rotated but still rejected.** `claude mcp remove twillingate` and
add it again — the old header is cached in the config, not re-read from your
shell.

---

## Operate and recover

### Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash -s -- --yes && sudo systemctl restart twillingate` |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin with `TWILLINGATE_VERSION=v26.825.1` in `.env`. |
| Apply migrations only | `twillingate migrate` |
| Export the registry | `twillingate config export > registry.json` |
| Import the registry | `twillingate config import registry.json` — also accepts a pre-upgrade `projects.json`; never archives or deletes anything absent from the file |
| Database size | `du -h /var/lib/twillingate/twillingate.db` |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |
| Recent config changes | `sqlite3 …/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |

On a systemd host every CLI command needs the environment the unit loads:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project list'
```

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`LOG_FILE`) is optional and off by default; if enabled,
install `deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Replication with litestream

The application does not replicate anything. `serve` writes a SQLite file
and `dashboards` reads one; moving that file off the machine is a deployment
choice. You do not need any of this for a single server you are willing to
back up some other way — nothing in the collector requires object storage,
and the default compose file has no credentials in it.

Litestream streams the SQLite WAL to S3-compatible storage as it is written,
so the copy in the bucket is seconds behind rather than a day behind like a
nightly dump. That buys backup, and a read replica for the two-server
topology — both sides make outbound HTTPS connections and neither needs to
reach the other.

**Bucket and credentials.** Any S3-compatible store works; these use
Cloudflare R2.

1. Create a bucket, for example `twillingate-backup`.
2. Create an API token with **Object Read & Write** — the *writer's*
   credential.
3. For a second machine, create a second token with **Object Read** only. A
   reader that cannot write cannot corrupt the backup, however wrong its
   configuration turns out to be.

Both go in `twillingate.env`, never in a config file:

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

**Configuration.** `deploy/litestream/litestream.yml` is the whole of it:

```yaml
dbs:
  - path: /var/lib/twillingate/twillingate.db
    replicas:
      - type: s3
        bucket: ${R2_BUCKET}
        path: litestream
        endpoint: ${R2_ENDPOINT}
        sync-interval: 5s
```

> **`path:` is the identity of the replica in the bucket.** A restore asks
> for the database by the path it had on the machine that wrote it. Every
> side — writer, reader, recovery host — must use the same value byte for
> byte, even when the file lives somewhere else locally. Getting this wrong
> produces an empty restore rather than an error, which is the single most
> common way to be surprised here.

> **Writer and reader must run the same litestream major/minor version.**
> 0.5 stores backups in a new bucket format (LTX) that 0.3 cannot see, and
> vice versa — a mismatched restore reports `no matching backups found`
> rather than an error, which looks exactly like an empty bucket. The
> compose files pin `litestream/litestream:0.5`; if you pin a different
> version anywhere, pin it everywhere.

**On the writer.** For docker, uncomment the `litestream` service in
`docker-compose.yml`, copy `litestream.yml` next to the compose files and
put the four variables in `.env`. For systemd, `install.sh` installs
`litestream.service` and `/etc/litestream.yml` but not the binary — take it
from <https://litestream.io/install/>, then `sudo systemctl enable --now
litestream`.

### Backup restore drill — do this monthly

A backup you have never restored is not a backup.

```bash
# What is in the bucket? (0.5 syntax; replaces the old snapshots/generations)
litestream ltx -config /etc/litestream.yml /var/lib/twillingate/twillingate.db

litestream restore -config /etc/litestream.yml -o /tmp/check.db \
  /var/lib/twillingate/twillingate.db

# It must be a valid database, not just a file that exists:
sqlite3 /tmp/check.db 'PRAGMA quick_check;'          # expect: ok
sqlite3 /tmp/check.db 'SELECT COUNT(*) FROM projects;'
sqlite3 /tmp/check.db "SELECT MAX(day) FROM v_web_daily;"
rm /tmp/check.db
```

`deploy/litestream/restore.sh` performs the same restore-and-verify — point
`REPLICA_PATH` at a scratch file to use it as the drill.

Check the max day is recent. A restore that succeeds but is days stale means
litestream is not replicating: inspect `journalctl -u litestream`. A restore
reporting `no matching backups found` against a bucket that is being written
usually means a version mismatch.

### Disaster recovery — the host is gone

```bash
# On a fresh host:
git clone <repo> && cd twillingate && make build
sudo ./deploy/systemd/install.sh --user twillingate --yes
sudo vi /etc/twillingate/twillingate.env       # same R2 credentials

# Restore BEFORE starting the collector, so it does not create an empty db:
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'

sudo systemctl start twillingate litestream
```

> **Restore first, always.** Starting `twillingate serve` against an empty
> data directory creates a fresh database, and litestream would then
> replicate that empty database over the good backup.

Two things do not survive: the visitor salt (already rotated daily by
design, so at most one day of visitor continuity is lost) and any events
still buffered in memory when the host died — bounded by
`BUFFER_FLUSH_INTERVAL`.

### Migrating one server to two

1. Stand up the VPS, pointing litestream at the same bucket.
2. On the machine that will render dashboards, stop the `twillingate`
   service and any litestream writer.
3. Install `restore.sh` on cron with `SOURCE_DB` set to the database path
   **on the VPS** — it must match `path:` in `litestream.yml`, not wherever
   the file lands locally.
4. Drop `docker-compose.yml` from this machine and run
   `docker-compose.evidence.yml` alone, with `DASHBOARDS_DB_PATH` and the
   restore's `REPLICA_PATH` both set to `/data/replica.db`. Confirm the
   first restore lands before the next rebuild.
5. Repoint the tracking snippet's `src` at the VPS hostname.
