#!/bin/sh
# Refresh a local read replica from object storage.
#
# The application does not do this: how a database gets from the machine that
# writes it to the machine that reads it is a deployment choice. This script
# is the litestream answer, and it is what `analytics dashboards` expects to
# find at REPLICA_PATH.
#
# The restore never writes to REPLICA_PATH directly. A failed download, a
# truncated file or a corrupt database leaves the previous replica in place,
# so the dashboards keep serving stale-but-valid data instead of nothing.
#
# Environment:
#   SOURCE_DB     path of the database *on the writer* — litestream keys a
#                 replica by it, so this must match the `path:` in
#                 litestream.yml exactly (default /var/lib/analytics/analytics.db)
#   REPLICA_PATH  where to put the local copy (default /var/lib/analytics/replica.db)
#   LITESTREAM_CONFIG  config file (default /etc/litestream.yml)
#   LOOP_INTERVAL seconds between runs; unset means run once and exit, which
#                 is what cron wants
#
# Requires: litestream, sqlite3, flock.
set -eu

SOURCE_DB="${SOURCE_DB:-/var/lib/analytics/analytics.db}"
REPLICA_PATH="${REPLICA_PATH:-/var/lib/analytics/replica.db}"
LITESTREAM_CONFIG="${LITESTREAM_CONFIG:-/etc/litestream.yml}"

restore_once() {
  tmp="$REPLICA_PATH.tmp"
  rm -f "$tmp"
  trap 'rm -f "$tmp"' EXIT

  litestream restore -config "$LITESTREAM_CONFIG" -o "$tmp" "$SOURCE_DB"

  check="$(sqlite3 "$tmp" 'PRAGMA quick_check' 2>&1)" || {
    echo "restore: quick_check failed to run: $check" >&2
    return 1
  }
  if [ "$check" != "ok" ]; then
    echo "restore: restored file is corrupt: $check" >&2
    return 1
  fi

  # Rename is atomic within a filesystem: a reader either sees the whole old
  # replica or the whole new one, never a half-written file.
  mv "$tmp" "$REPLICA_PATH"
  trap - EXIT
  echo "restore: replica updated at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

main() {
  if [ -n "${LOOP_INTERVAL:-}" ]; then
    while true; do
      restore_once || echo "restore: cycle failed, keeping the previous replica" >&2
      sleep "$LOOP_INTERVAL"
    done
  else
    restore_once
  fi
}

# Overlapping runs would race on the temporary file: cron must not start a
# second restore while a slow one is still running. The lock is held through
# an open file descriptor, so it is released even if this process is killed —
# a lock file left on disk cannot wedge replication.
exec 9>"$REPLICA_PATH.lock"
if command -v flock >/dev/null 2>&1 && ! flock -n 9; then
  echo "restore: another run is in progress, skipping" >&2
  exit 0
fi

main
