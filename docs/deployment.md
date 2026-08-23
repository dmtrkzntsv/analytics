# Deployment runbook

Two topologies. **All-in-one** puts ingestion, backup and dashboards on one
machine — simplest, and enough for a Pi at home behind a tunnel.
**Split** runs ingestion on a public VPS and dashboards at home off a verified
replica, so the dashboard machine never needs to be reachable from the
internet.

Both replicate SQLite to S3-compatible object storage (Cloudflare R2 below)
with litestream.

---

## 1. Object storage (R2)

1. Cloudflare dashboard → R2 → **Create bucket**, e.g. `analytics-backup`.
   Pick a location near the VPS; no public access.
2. R2 → **Manage API Tokens** → *Create API Token*, permission
   **Object Read & Write**, scoped to that bucket. Copy the Access Key ID and
   Secret Access Key — the secret is shown once.
3. Note the S3 endpoint: `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`.

Any S3-compatible store works; only `endpoint` and `bucket` change.

---

## 2. VPS install (both topologies)

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

## 3. Backoffice

### All-in-one

Ingestion, litestream and Evidence in one compose stack:

```bash
cd backoffice
cp ../deploy/projects.example.json projects.json    # edit projects
cat > .env <<'EOF'
R2_BUCKET=analytics-backup
R2_ENDPOINT=https://ACCOUNT_ID.r2.cloudflarestorage.com
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
EOF
docker compose -f docker-compose.aio.yml up -d
```

Evidence reads the live database read-only; SQLite WAL allows this alongside
the writer.

### Split

On the backoffice machine, only sync and Evidence run:

```bash
cd backoffice
cp ../deploy/projects.example.json projects.json
cat > .env <<'EOF'
R2_BUCKET=analytics-backup
R2_ENDPOINT=https://ACCOUNT_ID.r2.cloudflarestorage.com
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
EOF
```

Then `docker compose up -d` (the default `docker-compose.yml`); the compose
file itself sets `SYNC_REPLICA_PATH=/data/replica.db` and
`SYNC_LITESTREAM_CONFIG=/etc/litestream.yml` to match its mounts. The `sync`
service restores from R2 into `/data/replica.db.tmp`, verifies it with
`PRAGMA quick_check`, swaps it into place and touches `/data/.last_sync`.
Evidence watches that marker and rebuilds when it changes.

A failed or corrupt restore is not fatal: the temporary file is discarded and
the previous replica keeps serving. Check progress with
`docker compose logs -f sync`.

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

Without `sqlite3` installed, `analytics sync` performs the same
restore-and-verify against a scratch path — run it once with
`SYNC_REPLICA_PATH=/tmp/check.db` (plus the usual env) and inspect the
result.

Check the max day is recent. A restore that succeeds but is days stale means
litestream is not replicating: inspect `journalctl -u litestream`.

---

## 5. Migrating all-in-one → split

1. Stand up the VPS per section 2, pointing litestream at the same bucket.
2. On the backoffice machine, stop the `analytics` and `litestream` services
   in the aio compose file (or switch to `docker-compose.yml`).
3. Change Evidence's source to the replica:
   `EVIDENCE_SOURCE__analytics__filename=../../../data/replica.db`.
4. Bring up the `sync` service and confirm `/data/.last_sync` appears.
5. Repoint the tracking snippet's `src` at the VPS hostname.

The path is relative because the Evidence SQLite plugin resolves `filename`
against the source directory, not the project root.

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
| Upgrade | `curl -fsSL …/deploy/install.sh \| sudo bash -s -- --yes && sudo systemctl restart analytics` (or `make build && sudo ./deploy/install.sh --yes` from a checkout) |
| Apply migrations only | `sudo -u analytics sh -ac '. /etc/analytics/analytics.env; analytics migrate'` |
| Database size | `du -h /var/lib/analytics/analytics.db` |
| Replication status | `journalctl -u litestream --since -1h` |

Aggregation, pruning and incremental vacuum run daily at 03:00 UTC, and the
visitor salt rotates at 00:00 UTC. A catch-up pass runs at startup, so
downtime across those times does not skip a day.

File logging (`log.file`) is optional and off by default; if enabled, install
`deploy/logrotate/analytics` into `/etc/logrotate.d/`.
