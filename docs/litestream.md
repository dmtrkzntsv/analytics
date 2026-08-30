# Replication with litestream

The application does not replicate anything. `twillingate serve -api` writes a
SQLite file and `twillingate dashboards` reads a SQLite file; moving that file
off the machine, or onto a second one, is a deployment choice. This document
describes the choice most installations make.

You do not need any of this for a single server that you are willing to back
up some other way — or not at all. Nothing in the collector requires object
storage, and the default compose file has no credentials in it.

## What it gives you

Litestream streams the SQLite WAL to S3-compatible storage as it is written,
so the copy in the bucket is seconds behind the live database rather than a
day behind like a nightly dump. Two things fall out of that:

- **Backup.** The database can be restored onto a fresh host after the
  original is gone.
- **A read replica.** A second machine can restore periodically and render
  dashboards from its own copy, with the bucket as the only channel between
  the two — both sides make outbound HTTPS connections and neither needs to
  reach the other.

## 1. Bucket and credentials

Any S3-compatible store works; these instructions use Cloudflare R2.

1. Create a bucket, for example `twillingate-backup`.
2. Create an API token with **Object Read & Write** for it. This is the
   *writer's* credential.
3. If you run a second machine for dashboards, create a second token with
   **Object Read** only. A reader that cannot write cannot corrupt the
   backup, however wrong its configuration turns out to be.

Both go in the environment, never in a config file:

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

## 2. `litestream.yml`

`deploy/litestream/litestream.yml` is the whole configuration:

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

**`path:` is the identity of the replica in the bucket.** A restore asks for
the database by the path it had on the machine that wrote it. Every side —
the writer, any reader, a recovery host — must use the same value, byte for
byte, even when the file lives somewhere else locally. Getting this wrong
produces an empty restore rather than an error, which is the single most
common way to be surprised here.

Raise `sync-interval` on a Raspberry Pi or a metered connection; the cost is
how much data a sudden host failure can lose.

## 3. On the machine that writes

**Docker.** `docker-compose.yml` ships a commented `litestream` service —
uncomment it and bring the stack up again. It runs one `litestream/litestream`
container against the same volume. Copy `deploy/litestream/litestream.yml` next
to the compose files and put the four variables in `.env`.

**systemd.** `install.sh` installs `litestream.service` and
`/etc/litestream.yml`, but not the binary — take it from
<https://litestream.io/install/>, then:

```bash
sudo systemctl enable --now litestream
journalctl -u litestream -f
```

The credentials live in `/etc/twillingate/twillingate.env`, which both units load.

**Writer and reader must run the same litestream major/minor version.**
Litestream 0.5 stores backups in a new bucket format (LTX) that 0.3 cannot
see, and vice versa — a mismatched restore reports `no matching backups
found` rather than an error, which looks exactly like an empty bucket. The
compose files pin `litestream/litestream:0.5` to match what
<https://litestream.io/install/> currently installs; if you pin a different
version anywhere, pin it everywhere — the writer, every reader, and any
recovery host.

## 4. On the machine that reads

The reader restores a fresh copy periodically and points `dashboards` at it.
`deploy/litestream/restore.sh` does one cycle: restore to a temporary file,
verify it with `PRAGMA quick_check`, and only then rename it into place. A
failed download or a corrupt file leaves the previous replica serving, which
is the entire reason not to restore over the live path directly.

```bash
sudo install -m 0755 deploy/litestream/restore.sh /usr/local/bin/restore.sh
sudo cp deploy/litestream/restore.cron /etc/cron.d/twillingate-restore
```

It needs `litestream`, `sqlite3` and (optionally, for the overlap guard)
`flock` on the host. Configuration is environmental:

| Variable | Meaning |
| --- | --- |
| `SOURCE_DB` | The database path **on the writer** — must equal `path:` in `litestream.yml`. Default `/var/lib/twillingate/twillingate.db`. |
| `REPLICA_PATH` | Where the verified copy lands locally. Default `/var/lib/twillingate/replica.db`. Unrelated to `SOURCE_DB`, and must match the dashboards' `DASHBOARDS_DB_PATH`. |
| `LITESTREAM_CONFIG` | Default `/etc/litestream.yml`. |
| `LOOP_INTERVAL` | Seconds between cycles. Unset means run once and exit, which is what cron wants. |

Then run dashboards against that file:

```bash
echo DASHBOARDS_DB_PATH=/data/replica.db >> .env
docker compose -f docker-compose.evidence.yml up -d
```

The default is `/data/twillingate.db`, which is right when the volume is shared
with a collector; reading a restored replica instead has to be asked for.

`dashboards` notices the replica has been replaced by comparing size and
modification time, so it rebuilds within a minute of a successful restore
without any coordination between the two.

If you would rather not have cron on the host, `docker-compose.evidence.yml`
carries a commented `restore` service that runs the same script on a loop
inside a container, already pointed at the right paths and running as uid 10001
so the replica is owned by the user the dashboards run as. Use that or host
cron, never both, and neither alongside the collector — a restore would
overwrite the live database.

## 5. Verifying

A backup you have never restored is not a backup. Monthly, on the writer:

```bash
litestream ltx -config /etc/litestream.yml /var/lib/twillingate/twillingate.db
litestream restore -config /etc/litestream.yml -o /tmp/check.db \
  /var/lib/twillingate/twillingate.db
sqlite3 /tmp/check.db 'PRAGMA quick_check;'                 # expect: ok
sqlite3 /tmp/check.db 'SELECT MAX(day) FROM v_web_daily;'   # expect: recent
rm /tmp/check.db
```

`litestream ltx` lists the LTX files in the bucket — recent timestamps mean
replication is alive. (Litestream 0.5 removed the 0.3-era `snapshots` and
`generations` subcommands; `ltx`, `info` and `status` replace them.)

A restore that succeeds but is days stale means replication has stopped:
check `journalctl -u litestream` or `docker compose logs litestream`. A
restore that reports `no matching backups found` against a bucket you know
is being written usually means a version mismatch — see §3.

## 6. Recovering onto a fresh host

**Restore before starting the collector.** `twillingate serve -api` creates an
empty database if none exists, and litestream would then happily replicate
that empty database over your backup.

Install the same litestream major/minor version the writer ran — a newer or
older one cannot see the backup and reports `no matching backups found`, as
if the bucket were empty (see §3).

```bash
# install the binary and config first — see docs/deployment.md
sudo -u twillingate litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'
sudo systemctl start twillingate litestream
```

The `-o` path and the trailing argument differ in meaning: the first is where
the file goes on this host, the second is the identity it had on the old one.

Two things do not survive, by design: the visitor salt (rotated daily anyway,
so at most one day of visitor continuity is lost) and any events still
buffered in memory when the host died, bounded by `BUFFER_FLUSH_INTERVAL`.
