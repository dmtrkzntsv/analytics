# Deployment runbook

Two topologies. **One server** puts ingestion and dashboards on the same
machine — simplest, and enough for a Pi at home behind a tunnel. **Two
servers** runs ingestion on a public VPS and dashboards at home off a
restored replica, so the dashboard machine never needs to be reachable from
the internet.

Replication is not part of the application: `serve` writes a SQLite file and
`dashboards` reads one. The two-server topology needs something to move that
file, and litestream is the supported answer —
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
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/install.sh | sudo bash

# Or from a checkout:
git clone <repo> && cd analytics
make build
sudo ./install.sh                 # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version vYY.MMDD.{build}` to pin) and verifies its
SHA256 before installing. Releases are published by CI on every push to
`main`.

The installer creates a system account, installs the binary to
`/usr/local/bin/analytics`, creates `/var/lib/analytics` (0750, owned by the
service account), installs example `analytics.env` (infra settings + R2 credentials, loaded
by both units via `EnvironmentFile=`) and `projects.json` files, renders
both systemd units with the chosen user, and enables them. It never
overwrites an existing analytics.env or projects.json, so re-running it to
deploy a new binary is safe.

Then edit the two files it flagged:

```bash
sudo vi /etc/analytics/projects.json    # projects, allowed_origins
sudo vi /etc/analytics/analytics.env    # R2 credentials from step 1, geo
```

Install litestream (<https://litestream.io/install/>) if the installer
reported it missing, then:

```bash
sudo systemctl enable --now litestream
sudo systemctl start analytics
systemctl status analytics litestream
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

Ingestion and dashboards in one compose stack, no credentials needed:

```bash
mkdir analytics && cd analytics
base=https://raw.githubusercontent.com/dmtrkzntsv/analytics/main
curl -fsSLO $base/deploy/compose/docker-compose.yml
curl -fsSL $base/projects.example.json -o projects.json
docker compose up -d
```

`:3000` answers `503` until Evidence finishes its first build — roughly a
minute — then serves the site. To add continuous backup, fetch
`docker-compose.litestream.yml` and `litestream.yml`, put the R2 credentials
in `.env`, and bring the stack up with both files:

```bash
docker compose -f docker-compose.yml -f docker-compose.litestream.yml up -d
```

### Two servers

The reader restores the database on cron and renders it. Install
`deploy/litestream/restore.sh` and `restore.cron` on the host (see
[docs/litestream.md](litestream.md) §4), then:

```bash
curl -fsSLO $base/deploy/compose/docker-compose.evidence.yml
docker compose -f docker-compose.evidence.yml up -d
```

`dashboards` rebuilds within a minute of a successful restore: it compares
the replica's size and modification time and does not need to be told.

A failed or corrupt restore is not fatal — `restore.sh` verifies into a
temporary file and only then renames, so the previous replica keeps serving.

---

## 4. Backup restore drill — do this monthly

A backup you have never restored is not a backup. On the VPS:

```bash
litestream restore -config /etc/litestream.yml -o /tmp/check.db \
  /var/lib/analytics/analytics.db

# It must be a valid database, not just a file that exists:
sqlite3 /tmp/check.db 'PRAGMA quick_check;'          # expect: ok
sqlite3 /tmp/check.db 'SELECT COUNT(*) FROM projects;'
sqlite3 /tmp/check.db "SELECT MAX(day) FROM v_web_daily;"
rm /tmp/check.db
```

`deploy/litestream/restore.sh` performs the same restore-and-verify — point
`REPLICA_PATH` at a scratch file to use it as the drill.

Check the max day is recent. A restore that succeeds but is days stale means
litestream is not replicating: inspect `journalctl -u litestream`.

---

## 5. Migrating one server → two

1. Stand up the VPS per section 2, pointing litestream at the same bucket.
2. On the machine that will render dashboards, stop the `analytics` service
   (and the litestream overlay, if you were replicating from here).
3. Install `restore.sh` on cron per [docs/litestream.md](litestream.md) §4,
   with `SOURCE_DB` set to the database path **on the VPS** — it must match
   `path:` in `litestream.yml`, not wherever the file lands locally.
4. Switch to `docker-compose.evidence.yml`, which points
   `DASHBOARDS_DB_PATH` at `/data/replica.db`, and confirm the first restore
   lands before the next rebuild.
5. Repoint the tracking snippet's `src` at the VPS hostname.

---

## 6. Disaster recovery — the VPS is gone

```bash
# On a fresh host:
git clone <repo> && cd analytics && make build
sudo ./install.sh --user analytics --yes
sudo vi /etc/analytics/projects.json
sudo vi /etc/analytics/analytics.env       # same R2 credentials

# Restore before starting the collector, so it does not create an empty db:
sudo -u analytics litestream restore -config /etc/litestream.yml \
  -o /var/lib/analytics/analytics.db /var/lib/analytics/analytics.db
sudo -u analytics sqlite3 /var/lib/analytics/analytics.db 'PRAGMA quick_check;'

sudo systemctl start analytics litestream
```

Restore first, always. Starting `analytics serve` against an empty data
directory creates a fresh database, and litestream would then replicate that
empty database over the good backup.

Two things do not survive: the visitor salt (already rotated daily by design,
so at most one day of visitor continuity is lost) and any events still
buffered in memory when the host died — bounded by `BUFFER_FLUSH_INTERVAL`.

---

## 7. Routine operations

| Task | Command |
| --- | --- |
| Logs | `journalctl -u analytics -f` |
| Restart | `systemctl restart analytics` |
| Upgrade (systemd) | `curl -fsSL …/install.sh \| sudo bash -s -- --yes && sudo systemctl restart analytics` (or `make build && sudo ./install.sh --yes` from a checkout) |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin a release with `ANALYTICS_VERSION=v26.825.1` in `.env`. |
| Apply migrations only | `sudo -u analytics sh -ac '. /etc/analytics/analytics.env; analytics migrate'` |
| Generate an ingest key | `analytics keygen -n 1` |
| Database size | `du -h /var/lib/analytics/analytics.db`. Dashboards need room for one more copy: each rebuild snapshots the database into `DASHBOARDS_WORK_DIR`. |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`log.file`) is optional and off by default; if enabled, install
`deploy/logrotate/analytics` into `/etc/logrotate.d/`.

---

## 8. Upgrading to app analytics

This release adds app ingestion and changes the ingest surface. Six things
break, and two of them require a coordinated deploy — read this before
upgrading a running install.

### Breaking changes

1. **`ingest_keys` is required.** The service refuses to start until every
   project in `projects.json` has at least one. This is deliberate: the
   gentler alternative — key optional, warn when absent — leaves a silently
   unauthenticated project, which is the exact condition the change exists to
   remove.
2. **Every embedded snippet must be rewritten.** `data-key` and
   `data-identity` replace `data-project`. Old snippets receive `401`.
3. **`/api/hit` and `/api/event` are removed**, not deprecated. Everything
   goes to `POST /api/events`.
4. **An unknown project returns `401`**, not `204`.
5. **`identity: "anonymous"` (the new default) salts the site-supplied web
   `user_id`.** A site calling `analytics.identify("u_123")` previously wrote
   `u_123` straight into the database as a persistent cross-day identifier
   governed by nothing. Under the new default it is salted and rotated, so
   **cross-day web funnels and any query joining product events across days
   stop working** until that project sets `identity: "identified"`. This is a
   behaviour change on data you may already be collecting, not a config
   rename.
6. Migrations 003 and 004 rename `web_hits.visitor_hash` to `actor_id` and
   `product_events.user_id` to `actor_id`. Any custom SQL you have written
   against those columns needs updating. The rename is deliberate: in
   identified mode a column named `…_hash` would hold plaintext identifiers.

### Order of operations

Steps 2 and 3 are the coordinated pair. Between them, old snippets get `401`,
so schedule them together — ideally within the same maintenance window.

```bash
# 1. Generate a key per client and add them to projects.json.
analytics keygen -n 2
sudo -e /etc/analytics/projects.json

# 2. Deploy the new binary. Migrations 003/004 run on boot.
curl -fsSL https://…/install.sh | sudo bash -s -- --yes
sudo systemctl restart analytics

# 3. Update every site snippet, then deploy the sites.
#    <script defer src="https://…/js/script.js"
#            data-key="ak_…" data-identity="anonymous"></script>

# 4. Confirm traffic is arriving under each key label.
journalctl -u analytics -f | grep 'ingest summary'
```

Step 4 is also how you retire a key later: watch its label fall to zero, then
set `"disabled": true`.

### Rolling back

Migrations are forward-only. Restore from a litestream replica if you need to
go back — see §6.
