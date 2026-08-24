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
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/deploy/install.sh | sudo bash

# Or from a checkout:
git clone <repo> && cd analytics
make build
sudo ./deploy/install.sh          # --user NAME to skip the prompt, --yes for defaults
```

The curl form detects the architecture, downloads the matching tarball from
the latest GitHub release (`--version vX.Y.Z` to pin) and verifies its
SHA256 before installing. While the repository is private it needs a token:
prepend `-H "Authorization: Bearer $GITHUB_TOKEN"` to the curl and run
`sudo GITHUB_TOKEN="$GITHUB_TOKEN" bash`. Releases are published by CI on
every `v*` tag.

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
sudo ./deploy/install.sh --user analytics --yes
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
| Upgrade (systemd) | `curl -fsSL …/deploy/install.sh \| sudo bash -s -- --yes && sudo systemctl restart analytics` (or `make build && sudo ./deploy/install.sh --yes` from a checkout) |
| Upgrade (compose) | `docker compose pull && docker compose up -d`. Never `down -v`: the database lives in the named volume. Pin a release with `ANALYTICS_VERSION=v0.3.0` in `.env`. |
| Apply migrations only | `sudo -u analytics sh -ac '. /etc/analytics/analytics.env; analytics migrate'` |
| Database size | `du -h /var/lib/analytics/analytics.db`. Dashboards need room for one more copy: each rebuild snapshots the database into `DASHBOARDS_WORK_DIR`. |
| Replication status | `journalctl -u litestream --since -1h`, or `docker compose logs litestream` |
| Dashboard rebuilds | `docker compose logs dashboards` — one `dashboards: rebuilt` line per successful build |

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`log.file`) is optional and off by default; if enabled, install
`deploy/logrotate/analytics` into `/etc/logrotate.d/`.
