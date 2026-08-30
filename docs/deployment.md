# Deployment runbook

Two topologies. **One server** puts ingestion and dashboards on the same
machine — simplest, and enough for a Pi at home behind a tunnel. **Two
servers** runs ingestion on a public VPS and dashboards at home off a
restored replica, so the dashboard machine never needs to be reachable from
the internet.

Replication is not part of the application: `serve` writes a SQLite
file and `dashboards` reads one. The two-server topology needs something to
move that file, and litestream is the supported answer —
[docs/litestream.md](litestream.md) covers bucket setup, credentials, the
writer, the reader and recovery. The one-server topology needs none of it.

---

## 1. Object storage (R2)

Only if you want backup or a second machine. See
[docs/litestream.md](litestream.md) §1 for the bucket, the read-write token
for the writer and the read-only token for the reader. Any S3-compatible
store works; only `endpoint` and `bucket` change.

---

## 2. VPS install (two-server topology)

```bash
# From a published release (no checkout needed):
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash

# Or from a checkout:
git clone <repo> && cd twillingate
make build
sudo ./deploy/systemd/install.sh                 # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version vYY.MMDD.{build}` to pin) and verifies its
SHA256 before installing. Releases are published by CI on every push to
`main`.

The installer creates a system account, installs the binary to
`/usr/local/bin/twillingate`, creates `/var/lib/twillingate` (0750, owned by the
service account), installs an example `twillingate.env` (infra settings + R2
credentials, loaded by both units via `EnvironmentFile=`), renders both
systemd units with the chosen user, and enables them. It never overwrites an
existing twillingate.env, so re-running it to deploy a new binary is safe.

Then edit the file it flagged, and create your first project — projects live
in the database, not a shipped file:

```bash
sudo vi /etc/twillingate/twillingate.env    # R2 credentials from step 1, geo
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -alias myapp'
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project myapp -label web'
```

Install litestream (<https://litestream.io/install/>) if the installer
reported it missing — currently v0.5.x, which is also what the compose files
pin; writer and reader must run the same major/minor version (see
[docs/litestream.md](litestream.md) §3) — then:

```bash
sudo systemctl enable --now litestream
sudo systemctl start twillingate
systemctl status twillingate litestream
curl -s localhost:8080/healthz
```

Put TLS in front of `127.0.0.1:8080` — Caddy, nginx, or a Cloudflare tunnel.
The service deliberately binds to loopback by default.

### Verifying ingestion

```bash
curl -i -X POST http://localhost:8080/api/hit \
  -H 'Origin: https://myapp.com' \
  -d '{"project":"myapp","url":"https://myapp.com/"}'      # expect 202

curl -i -X POST http://localhost:8080/api/hit \
  -H 'Origin: https://not-allowed.com' \
  -d '{"project":"myapp","url":"https://myapp.com/"}'      # expect 403
```

---

## 3. Dashboards

### One server

Tracking (`docker-compose.yml` — ingestion, the tracker script and, once
`MCP_AUTH_DSN` is set, `/mcp`) and reporting (`docker-compose.evidence.yml`)
as one project sharing one database. No credentials needed:

```bash
mkdir twillingate && cd twillingate
base=https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
echo COMPOSE_FILE=docker-compose.yml:docker-compose.evidence.yml > .env
docker compose up -d
docker compose exec twillingate twillingate project create -alias myapp
docker compose exec twillingate twillingate key issue -project myapp -label web
```

`COMPOSE_FILE` is what lets every later `docker compose` command see both
files; without it, pass `-f` twice each time. `:3000` answers `503` until
Evidence finishes its first build — roughly a minute — then serves the site.

To add continuous backup, copy `deploy/litestream/litestream.yml` next to the
compose files, put the R2 credentials in `.env`, and uncomment the
`litestream` service in `docker-compose.yml` — the writer half is shipped
commented out rather than as a separate overlay file.

### Two servers

The reader restores the database on cron and renders it. Install
`deploy/litestream/restore.sh` and `restore.cron` on the host (see
[docs/litestream.md](litestream.md) §4), then:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
docker compose -f docker-compose.evidence.yml up -d
```

Set `DASHBOARDS_DB_PATH=/data/replica.db` in `.env`: the file defaults to
`/data/twillingate.db`, which is the shared-volume case, not this one. In place
of host cron it also carries a commented `restore` service — use one or the
other, never both.

`dashboards` rebuilds within a minute of a successful restore: it compares
the replica's size and modification time and does not need to be told.

A failed or corrupt restore is not fatal — `restore.sh` verifies into a
temporary file and only then renames, so the previous replica keeps serving.

---

## 4. Backup restore drill — do this monthly

A backup you have never restored is not a backup. On the VPS:

```bash
# What is in the bucket? (0.5 syntax; replaces the old `snapshots`/`generations`)
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
that reports `no matching backups found` against a bucket that is being
written usually means the litestream running the drill is a different
major/minor version than the writer — see
[docs/litestream.md](litestream.md) §3.

---

## 5. Migrating one server → two

1. Stand up the VPS per section 2, pointing litestream at the same bucket.
2. On the machine that will render dashboards, stop the `twillingate` service
   (and the litestream overlay, if you were replicating from here).
3. Install `restore.sh` on cron per [docs/litestream.md](litestream.md) §4,
   with `SOURCE_DB` set to the database path **on the VPS** — it must match
   `path:` in `litestream.yml`, not wherever the file lands locally.
4. Drop `docker-compose.yml` from this machine and run
   `docker-compose.evidence.yml` alone, with `DASHBOARDS_DB_PATH` and the
   restore's `REPLICA_PATH` both set to `/data/replica.db`, and confirm the
   first restore lands before the next rebuild.
5. Repoint the tracking snippet's `src` at the VPS hostname.

---

## 6. Disaster recovery — the VPS is gone

```bash
# On a fresh host:
git clone <repo> && cd twillingate && make build
sudo ./deploy/systemd/install.sh --user twillingate --yes
sudo vi /etc/twillingate/twillingate.env       # same R2 credentials

# Restore before starting the collector, so it does not create an empty db:
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'

sudo systemctl start twillingate litestream
```

Restore first, always. Starting `twillingate serve` against an empty data
directory creates a fresh database, and litestream would then replicate that
empty database over the good backup.

Two things do not survive: the visitor salt (already rotated daily by design,
so at most one day of visitor continuity is lost) and any events still
buffered in memory when the host died — bounded by `BUFFER_FLUSH_INTERVAL`.

---

## 7. Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u twillingate -f` |
| Restart | `systemctl restart twillingate` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash -s -- --yes && sudo systemctl restart twillingate` (or `make build && sudo ./deploy/systemd/install.sh --yes` from a checkout) |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin a release with `TWILLINGATE_VERSION=v26.825.1` in `.env`. |
| Apply migrations only | `sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate migrate'` |
| Create a project | `sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -alias myapp'` |
| Issue an ingest key | `sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project myapp -label web'` |
| Export the registry (backup/inspect) | `sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate config export' > registry.json` |
| Import/migrate the registry | `sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate config import registry.json'` — also accepts a pre-upgrade `projects.json`; never archives or deletes anything absent from the file |
| Database size | `du -h /var/lib/twillingate/twillingate.db`. Dashboards need room for one more copy: each rebuild snapshots the database into `DASHBOARDS_WORK_DIR`. |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |
| Recent config changes (audit log) | `sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db "SELECT * FROM audit_log ORDER BY ts DESC LIMIT 20"` |

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`log.file`) is optional and off by default; if enabled, install
`deploy/logrotate/twillingate` into `/etc/logrotate.d/`.

### Enabling MCP on an installed host

Auth-mode setup (token / Cloudflare Access / generic OAuth IdP) is covered
step by step in [mcp-auth.md](mcp-auth.md).

The installer's unit runs bare `serve`, which starts MCP automatically the
moment its auth is configured — until then it logs "MCP endpoint disabled"
at boot and serves ingestion alone. Enabling MCP is therefore one edit:

```bash
sudo vi /etc/twillingate/twillingate.env
# Set MCP_AUTH_DSN (token://…, cloudflare://…?aud=… or oauth://…) — see
# mcp-auth.md. For token mode, mint the value first:
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate keygen -mcp'
sudo systemctl restart twillingate
```

Set `MCP_ADDR` to give MCP its own port; unset, it shares the ingestion
listener (bare `serve` logs a warning naming the shared address). To run
the surfaces as separate processes — independently restartable, scalable
and exposable — copy `deploy/systemd/twillingate.service` to e.g.
`twillingate-mcp.service`, change `ExecStart=` to `twillingate serve -mcp`
(explicitly requesting `-mcp` makes missing auth config a hard error),
and change the original unit's `ExecStart=` to `twillingate serve -api`.

A `serve -mcp`-only process still starts the store/jobs runner and so still
runs the daily aggregation pass against `DATABASE_DSN` — `-mcp` only makes
the HTTP listener conditional, not the background jobs. Point a `-mcp`-only
unit at a Litestream replica and it will write to that replica on every
pass. Set `MCP_DB_PATH` (the path MCP reads for queries) and `DATABASE_DSN`
(the path the aggregation pass writes to) deliberately for your topology:
either keep `DATABASE_DSN` on a database this process is meant to own, or
accept that a two-process topology (one `-api`, one `-mcp`, each with its
own `DATABASE_DSN`) runs the idempotent daily aggregation twice.

In `cloudflare` mode, the Access application sits in front of the MCP
hostname or path only — the ingestion path (`/api/events`) must stay outside
it so devices can keep posting without an Access session. Scope the
application narrowly (for example `twillingate.example.com/mcp`, not the bare
hostname), turn on "managed OAuth" for it, and build `MCP_AUTH_DSN`
(`cloudflare://<team>.cloudflareaccess.com?aud=<tag>`) from that
application. Access then serves the OAuth discovery
documents and the `401` challenge at the edge; the binary only validates the
`Cf-Access-Jwt-Assertion` header Access forwards, and requests that reach the
origin without having passed Access are rejected.
